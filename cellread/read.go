// Package cellread reads monospace text, such as code in a terminal, editor
// or code block, from a screenshot. It needs no OCR engine, model or network:
// it finds the character grid in the pixels, works out which installed font
// drew the text, and matches each cell against that font's own glyphs.
//
// Experimental. It reads only ASCII and Latin-1 letters in upright, regular
// to bold monospace fonts installed on this machine, and assumes sharp screen
// pixels, not video or scans.
package cellread

import (
	"errors"
	"image"
	"maps"
	"math"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/vector"
	"golang.org/x/text/unicode/norm"
)

// ErrNoText means the image holds nothing that looks like text.
var ErrNoText = errors.New("no text found")

// ErrNotMonospace means the text doesn't keep to a grid of equal cells, as
// proportional text such as most web pages, chat and documents doesn't.
var ErrNotMonospace = errors.New("not monospace text")

// maxBorderInk is how much ink the cell borders may cross, relative to an
// average column of the text, for it to count as monospace. Code screenshots
// in four fonts measured at most 0.10, and mock web pages, chat, video calls,
// forms and documents, and a real chat screenshot, at least 0.25.
const maxBorderInk = 0.17

// maxWideScore is the most either cell of a pair crossed by one glyph may
// match a Latin character for the pair to be read as one glyph two cells
// wide. The halves of Chinese, Japanese and Korean glyphs in Ghostty matched
// 0.72 at most, and of a test's stand-in 0.78; letters that touch matched
// 0.91 and up, such as er, and =8 in a small chat screenshot 0.68 and 0.89.
const maxWideScore = 0.8

// Result is the text read from an image, and how sure the reader was.
type Result struct {
	// Text is what was read. Each Chinese, Japanese or Korean character, two
	// cells wide in a terminal, is a U+FFFD, for another reader to fill in
	// from Wide.
	Text string
	// Font is the installed font the text was matched against.
	Font string
	// Score is the mean match of every cell against its glyph, from -1 to 1.
	// Text in a font that isn't installed scores lower, but the ranges
	// overlap, so it isn't a reliable signal on its own.
	Score float64
	// Wide are the runs of characters two cells wide that the cell reader
	// left as U+FFFD, as it reads only Latin letters.
	Wide []Wide
}

// Wide is a run of characters two cells wide, such as Chinese, Japanese or
// Korean, on one line of a Result's text.
type Wide struct {
	Line   int // line of the text
	Start  int // position in the line, in runes, of the first U+FFFD
	Count  int // characters in the run
	Bounds image.Rectangle
}

// unread stands in for a wide character in Result.Text.
const unread = '\uFFFD'

type page struct {
	k        ink
	rows     []span
	pitch    float64
	x0       float64
	occ      [][]bool
	w, h     int
	baseline float64
	boxes    []image.Point // top left of each occupied cell
	where    [][2]int      // row and column of each occupied cell
	at       map[[2]int]int
	rowIDs   [][]int // indexes into boxes, per row that has any
	// wide are the cell pairs, by row and first column, that one glyph two
	// cells wide may cover, or a ligature.
	wide map[[2]int]bool
}

func newPage(img image.Image) (*page, bool) {
	k := newInk(img)
	k.eraseSlivers()
	bands := k.bands()
	if len(bands) == 0 {
		return nil, false
	}
	pitch, x0, ok := k.cellPitch(bands)
	if !ok {
		return nil, false
	}
	pg := &page{k: k, rows: trimEdgeRows(k.lineGrid()), pitch: pitch, x0: x0, w: int(math.Round(pitch)), at: map[[2]int]int{}}
	pg.occ = k.occupied(pg.rows, pitch, x0)
	pg.wide = k.widePairs(pg.rows, pitch, x0)
	for _, r := range pg.rows {
		pg.h = max(pg.h, r.b-r.a)
	}
	pg.baseline = float64(pg.h) * 0.75
	for r, row := range pg.occ {
		var ids []int
		for c, on := range row {
			if on {
				ids = append(ids, len(pg.boxes))
				pg.at[[2]int{r, c}] = len(pg.boxes)
				pg.boxes = append(pg.boxes, image.Pt(int(math.Round(x0+float64(c)*pitch)), pg.rows[r].a))
				pg.where = append(pg.where, [2]int{r, c})
			}
		}
		if ids != nil {
			pg.rowIDs = append(pg.rowIDs, ids)
		}
	}
	return pg, len(pg.boxes) > 0
}

// window cuts a w x h patch at p moved by (dx, dy), zero outside the image,
// as a unit vector.
func (k ink) window(p image.Point, w, h, dx, dy int) []float32 {
	out := make([]float32, w*h)
	x0, y0 := p.X+dx, p.Y+dy
	for y := 0; y < h; y++ {
		yy := y0 + y
		if yy < 0 || yy >= k.h {
			continue
		}
		for x := 0; x < w; x++ {
			xx := x0 + x
			if xx >= 0 && xx < k.w {
				out[y*w+x] = k.v[yy*k.w+xx]
			}
		}
	}
	return unit(out)
}

// unit centres v and scales it to length 1, so a dot product of two is their
// normalised correlation: 1 for the same shape at any contrast or colour.
func unit(v []float32) []float32 {
	var mean float64
	for _, x := range v {
		mean += float64(x)
	}
	mean /= float64(max(len(v), 1))
	var norm float64
	for i, x := range v {
		d := float64(x) - mean
		v[i] = float32(d)
		norm += d * d
	}
	norm = math.Max(math.Sqrt(norm), 1e-6)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// match finds each cell's best template over the given shifts.
func (pg *page) match(ids []int, tmpl [][]float32, dys, dxs []int) ([]float32, []int) {
	best := make([]float32, len(ids))
	idx := make([]int, len(ids))
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for n := range next {
				best[n] = -2
				for _, dy := range dys {
					for _, dx := range dxs {
						win := pg.k.window(pg.boxes[ids[n]], pg.w, pg.h, dx, dy)
						for t, v := range tmpl {
							if s := dot(win, v); s > best[n] {
								best[n], idx[n] = s, t
							}
						}
					}
				}
			}
		})
	}
	for n := range ids {
		next <- n
	}
	close(next)
	wg.Wait()
	return best, idx
}

// fitShift finds the one offset that best fits every cell in ids.
func (pg *page) fitShift(ids []int, tmpl [][]float32, dys, dxs []int) (dy, dx int, score float64) {
	type shift struct{ dy, dx int }
	var shifts []shift
	for _, y := range dys {
		for _, x := range dxs {
			shifts = append(shifts, shift{y, x})
		}
	}
	means := make([]float64, len(shifts))
	var wg sync.WaitGroup
	next := make(chan int)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for n := range next {
				total := 0.0
				for _, id := range ids {
					win := pg.k.window(pg.boxes[id], pg.w, pg.h, shifts[n].dx, shifts[n].dy)
					best := float32(-2)
					for _, v := range tmpl {
						best = max(best, dot(win, v))
					}
					total += float64(best)
				}
				means[n] = total / float64(max(len(ids), 1))
			}
		})
	}
	for n := range shifts {
		next <- n
	}
	close(next)
	wg.Wait()
	score = math.Inf(-1)
	for n, m := range means {
		if m > score {
			dy, dx, score = shifts[n].dy, shifts[n].dx, m
		}
	}
	return dy, dx, score
}

func mean32(v []float32) float64 {
	s := 0.0
	for _, x := range v {
		s += float64(x)
	}
	return s / float64(max(len(v), 1))
}

func span1(lo, hi, step int) []int {
	var out []int
	for v := lo; v <= hi; v += step {
		out = append(out, v)
	}
	return out
}

// floorDiv rounds towards minus infinity, so offsets are symmetric about zero.
func floorDiv(a, b int) int { return int(math.Floor(float64(a) / float64(b))) }

type reading struct {
	scores []float32
	picks  []int
	dy     []int // vertical offset of each cell's row
}

// readRows matches each row at its own vertical offset, since rows on a
// fractional line spacing round to different pixels.
func (pg *page) readRows(tmpl [][]float32, dy0 int, dxr []int) reading {
	n := len(pg.boxes)
	rd := reading{make([]float32, n), make([]int, n), make([]int, n)}
	for _, ids := range pg.rowIDs {
		bestDy, bestScore := dy0, math.Inf(-1)
		for d := dy0 - 3; d <= dy0+3; d++ {
			s, _ := pg.match(ids, tmpl, []int{d}, dxr)
			if m := mean32(s); m > bestScore {
				bestDy, bestScore = d, m
			}
		}
		s, p := pg.match(ids, tmpl, []int{bestDy}, dxr)
		for j, id := range ids {
			rd.scores[id], rd.picks[id], rd.dy[id] = s[j], p[j], bestDy
		}
	}
	return rd
}

type candidate struct {
	font   *Font
	size   float64
	dy, dx int
	score  float64
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func every(ids []int, step int) []int {
	var out []int
	for i := 0; i < len(ids); i += max(step, 1) {
		out = append(out, ids[i])
	}
	return out
}

// chooseFont runs a quick pass over every font, a careful pass over the best
// few families, then lets the top three compete by reading the whole page.
func (pg *page) chooseFont(fonts []*Font) (candidate, reading, []candidate) {
	all := make([]int, len(pg.boxes))
	for i := range all {
		all[i] = i
	}
	sample := every(all, len(all)/60)
	few := every(sample, len(sample)/20)
	dys := span1(floorDiv(-pg.h, 4), pg.h/4, 1)
	dxs := span1(floorDiv(-pg.w, 3), pg.w/3, 1)
	dys2, dxs2 := span1(dys[0], dys[len(dys)-1], 2), span1(dxs[0], dxs[len(dxs)-1], 2)

	type quick struct {
		f     *Font
		score float64
	}
	var quicks []quick
	for _, f := range fonts {
		t, _ := f.templates(round2(pg.pitch/f.Advance), pg.w, pg.h, pg.baseline, []float64{0}, false)
		// Every other offset first, then the neighbours of the best: a coarse
		// step alone can skip one font's baseline and land on another's.
		dy, dx, _ := pg.fitShift(few, t, dys2, dxs2)
		_, _, score := pg.fitShift(few, t, []int{dy - 1, dy, dy + 1}, []int{dx - 1, dx, dx + 1})
		quicks = append(quicks, quick{f, score})
	}
	sort.SliceStable(quicks, func(i, j int) bool { return quicks[i].score > quicks[j].score })

	// One family's weights would fill a shortlist, and its top-scoring weight is
	// often not the one on screen, so each family also sends its regular weight.
	var short []*Font
	fams := map[string]bool{}
	for _, q := range quicks {
		if fams[q.f.Family] || len(fams) == 8 {
			continue
		}
		fams[q.f.Family] = true
		short = append(short, q.f)
		for _, f := range fonts {
			if f.Family == q.f.Family && f != q.f && f.Weight == 400 {
				short = append(short, f)
				break
			}
		}
	}

	// Every glyph sits the same way in its cell, so one offset fits the page.
	// It is searched widely once, then only nearby for each candidate, since
	// fonts and sizes move glyphs by a pixel or two at most.
	lead := quicks[0].f
	t0, _ := lead.templates(round2(pg.pitch/lead.Advance), pg.w, pg.h, pg.baseline, []float64{0}, false)
	dyAll, dxAll, _ := pg.fitShift(sample, t0, dys, dxs)
	near := func(c, lo, hi int) []int { return span1(max(c-lo, -hi), min(c+lo, hi), 1) }
	var ranked []candidate
	for _, f := range short {
		for _, mul := range []float64{1, 0.985, 0.97, 1.015} {
			size := round2(pg.pitch / f.Advance * mul)
			t0, _ := f.templates(size, pg.w, pg.h, pg.baseline, []float64{0}, false)
			fit := candidate{font: f, size: size}
			fit.dy, fit.dx, fit.score = pg.fitShift(sample, t0, near(dyAll, 3, pg.h), near(dxAll, 2, pg.w))
			t, _ := pg.templatesFor(f, size, false)
			s, _ := pg.match(sample, t, []int{fit.dy - 1, fit.dy, fit.dy + 1}, []int{fit.dx})
			fit.score = mean32(s)
			ranked = append(ranked, fit)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	var best candidate
	var bestR reading
	bestMean := math.Inf(-1)
	// Finalists come from different families: three weights of one family
	// would otherwise fill every place and shut the real font out.
	// Some fonts are another under a new name, such as DejaVu Sans Mono and
	// Bitstream Vera Sans Mono: identical glyphs score the same to within
	// float rounding, so a repeated score and size is a copy and would waste a
	// place. Copies are kept aside instead, since they can differ in
	// ligatures alone, as JetBrains Mono and JetBrains Mono NL do.
	used := map[string]bool{}
	copies := map[[2]float64][]candidate{}
	var bestKey [2]float64
	for _, c := range ranked {
		key := [2]float64{math.Round(c.score * 1e6), c.size}
		if _, seen := copies[key]; seen {
			if !used[c.font.Family] {
				used[c.font.Family] = true
				copies[key] = append(copies[key], c)
			}
			continue
		}
		if used[c.font.Family] {
			continue
		}
		used[c.font.Family] = true
		copies[key] = nil
		t, _ := pg.templatesFor(c.font, c.size, false)
		rd := pg.readRows(t, c.dy, []int{c.dx - 1, c.dx, c.dx + 1})
		if m := mean32(rd.scores); m > bestMean {
			best, bestR, bestMean, bestKey = c, rd, m, key
		}
		if len(used) == 4 {
			break
		}
	}
	return best, bestR, copies[bestKey]
}

func (pg *page) templatesFor(f *Font, size float64, latin bool) ([][]float32, []rune) {
	return f.templates(size, pg.w, pg.h, pg.baseline, subPixel, latin)
}

// findOps finds runs of cells better read as one operator shape, which is how
// ligature fonts draw them. Along each run of cells it picks the best mix of
// single glyphs and operators.
func (pg *page) findOps(c candidate, rd reading) map[[2]int]rune {
	ops := c.font.operatorTemplates(c.size, pg.pitch, pg.h, pg.baseline)
	dxr := []int{c.dx - 1, c.dx, c.dx + 1}
	found := map[[2]int]rune{}
	for _, ids := range pg.rowIDs {
		r := pg.where[ids[0]][0]
		cols := map[int]bool{}
		for _, id := range ids {
			cols[pg.where[id][1]] = true
		}
		for _, id := range ids {
			start := pg.where[id][1]
			if cols[start-1] {
				continue
			}
			end := start
			for cols[end+1] {
				end++
			}
			type step struct {
				total float64
				back  int
				op    string
			}
			dp := map[int]step{start - 1: {}}
			for col := start; col <= end; col++ {
				single := float64(rd.scores[pg.at[[2]int{r, col}]])
				best := step{dp[col-1].total + single, col - 1, ""}
				for op, t := range ops {
					k := len(op)
					s := col - k + 1
					if s < start {
						continue
					}
					first := pg.at[[2]int{r, s}]
					wide := int(math.Round(pg.pitch * float64(k)))
					v := math.Inf(-1)
					for _, dx := range dxr {
						v = math.Max(v, float64(dot(pg.k.window(pg.boxes[first], wide, pg.h, dx, rd.dy[first]), t)))
					}
					singles := 0.0
					for j := range k {
						singles += float64(rd.scores[pg.at[[2]int{r, s + j}]])
					}
					singles /= float64(k)
					// An operator must clearly beat its own cells' readings to replace them.
					if v > math.Max(singles+0.03, 0.75) && dp[s-1].total+v*float64(k) > best.total {
						best = step{dp[s-1].total + v*float64(k), s - 1, op}
					}
				}
				dp[col] = best
			}
			for col := end; col >= start; {
				st := dp[col]
				for j, ch := range st.op {
					found[[2]int{r, st.back + 1 + j}] = ch
				}
				col = st.back
			}
		}
	}
	return found
}

// learn averages the cells the reader is surest of into templates of this
// screenshot's own rendering, which no drawn glyph matches exactly. Cells
// read as part of an operator stay out, or half an arrow would be learned as
// a real character.
func (pg *page) learn(t [][]float32, labels []rune, rd reading, ops map[[2]int]rune, dxr []int) ([][]float32, []rune) {
	sum := map[rune][]float32{}
	count := map[rune]int{}
	var order []rune
	for id, s := range rd.scores {
		if s < 0.85 {
			continue
		}
		if _, isOp := ops[pg.where[id]]; isOp {
			continue
		}
		ch := labels[rd.picks[id]]
		shift, bestS := dxr[0], float32(-2)
		for _, dx := range dxr {
			if v := dot(pg.k.window(pg.boxes[id], pg.w, pg.h, dx, rd.dy[id]), t[rd.picks[id]]); v > bestS {
				shift, bestS = dx, v
			}
		}
		win := pg.k.window(pg.boxes[id], pg.w, pg.h, shift, rd.dy[id])
		if sum[ch] == nil {
			sum[ch] = make([]float32, len(win))
			order = append(order, ch)
		}
		for i, v := range win {
			sum[ch][i] += v
		}
		count[ch]++
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	outT := append([][]float32{}, t...)
	outL := append([]rune{}, labels...)
	for _, ch := range order {
		v := sum[ch]
		for i := range v {
			v[i] /= float32(count[ch])
		}
		outT = append(outT, weigh(unit(v), ch))
		outL = append(outL, ch)
	}
	return outT, outL
}

// Read reads the text in img, matching it against fonts. Spaces and
// indentation come from the grid, so they are exact even where a glyph is
// misread.
func Read(img image.Image, fonts []*Font) (Result, error) {
	if len(fonts) == 0 {
		return Result{}, errNoFonts
	}
	p, err := Prepare(img)
	if err != nil {
		return Result{}, err
	}
	return p.Read(fonts)
}

// wideGlyphs picks the cell pairs that are one glyph two cells wide, by row
// and first column. A pair crossed by one glyph is one unless it is a
// ligature or its cells read as Latin letters: in unhinted text, er and f(
// can touch. A run of them then takes in the pairs either side whose cells
// both have ink and match nothing well, and whose outer borders no ink
// crosses, as some glyphs, such as い, don't cross the middle border. Without
// the borders, a run took ト's right half and the 0 after it as one glyph.
func (pg *page) wideGlyphs(rd reading, ops map[[2]int]rune) map[[2]int]bool {
	inOp := func(p [2]int) bool {
		_, ok := ops[p]
		return ok
	}
	score := func(p [2]int) float32 {
		if id, ok := pg.at[p]; ok {
			return rd.scores[id]
		}
		return 0
	}
	wide := map[[2]int]bool{}
	for pos := range pg.wide {
		right := [2]int{pos[0], pos[1] + 1}
		if !inOp(pos) && !inOp(right) && score(pos) < maxWideScore && score(right) < maxWideScore {
			wide[pos] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for _, pos := range slices.SortedFunc(maps.Keys(wide), cmpPos) {
			for _, next := range [][2]int{{pos[0], pos[1] + 2}, {pos[0], pos[1] - 2}} {
				right := [2]int{next[0], next[1] + 1}
				_, l := pg.at[next]
				_, r := pg.at[right]
				switch {
				case next[1] < 0 || wide[next] || wide[[2]int{next[0], next[1] - 1}] || wide[right]:
				case !l || !r, inOp(next), inOp(right):
				case pg.k.crossed(pg.rows[next[0]], pg.x0, pg.pitch, next[1]) > 1, pg.k.crossed(pg.rows[next[0]], pg.x0, pg.pitch, next[1]+2) > 1:
				case score(next) < maxWideScore && score(right) < maxWideScore:
					wide[next] = true
					grew = true
				}
			}
		}
	}
	return wide
}

// dropMissingMarks reads a cell as its letter without the accent when the
// accent isn't on screen. A template only scores how alike shapes are, and
// in Go Mono an I matched Ī better than I, and E matched Ē as well as E,
// with nothing above either; so a letter read with a mark keeps it only if
// there is ink where the mark is drawn.
func (pg *page) dropMissingMarks(f *Font, size float64, chars map[[2]int]rune, rd reading, dx int) {
	z := vector.NewRasterizer(pg.w, pg.h)
	draw := func(ch rune) []float32 {
		gid, ok := f.Glyphs[ch]
		if !ok {
			return nil
		}
		z.Reset(pg.w, pg.h)
		f.glyph(gid, size, 0, pg.baseline, unhinted, z)
		c := coverage(z, pg.w, pg.h)
		for i := range c {
			c[i] /= 255
		}
		return c
	}
	type mark struct {
		base     rune
		px, body []image.Point // where the mark is drawn, and the rest
	}
	marks := map[rune]*mark{}
	for id, pos := range pg.where {
		ch := chars[pos]
		if ch <= '~' {
			continue
		}
		m, ok := marks[ch]
		if !ok {
			m = &mark{}
			marks[ch] = m
			base := []rune(norm.NFD.String(string(ch)))[0]
			if a, b := draw(ch), draw(base); base <= '~' && a != nil && b != nil {
				m.base, m.px = base, markPixels(a, b, pg.w)
				for i := range a {
					if a[i] > 0.5 && b[i] > 0.5 {
						m.body = append(m.body, image.Pt(i%pg.w, i/pg.w))
					}
				}
			}
		}
		if len(m.px) > 0 && !pg.markInked(pg.boxes[id], m.px, m.body, dx, rd.dy[id]) {
			chars[pos] = m.base
		}
	}
}

// markPixels are where accented, drawn w wide, has ink that base, the
// letter without the accent, doesn't: those above or below base where there
// are any, as marks mostly are, or else all of them, as for í, whose acute
// takes the place of i's dot.
func markPixels(accented, base []float32, w int) []image.Point {
	top, bottom := len(base)/w, -1
	for i, v := range base {
		if v > 0.5 {
			top, bottom = min(top, i/w), max(bottom, i/w)
		}
	}
	var all, outside []image.Point
	for i := range accented {
		if accented[i]-base[i] > 0.5 {
			p := image.Pt(i%w, i/w)
			all = append(all, p)
			if p.Y < top-1 || p.Y > bottom+1 {
				outside = append(outside, p)
			}
		}
	}
	if len(outside) > 0 {
		return outside
	}
	return all
}

// markInked reports whether the cell at box, shifted as it was read, has
// ink where a mark is drawn at px. Hinting moves a glyph by a pixel or so,
// so the cell is first lined up on body, the strokes the letter shares with
// its base; then the mark is looked for there and nowhere else. Allowing a
// pixel's slack for the mark itself let a plain i's dot pass for the acute
// of í, a pixel away; lined up, false marks had no ink, and real ones in
// twelve languages had 0.39 or more.
func (pg *page) markInked(box image.Point, px, body []image.Point, dx, dy int) bool {
	inkOver := func(px []image.Point, dx, dy int) float64 {
		sum := 0.0
		for _, p := range px {
			x, y := box.X+dx+p.X, box.Y+dy+p.Y
			if x >= 0 && x < pg.k.w && y >= 0 && y < pg.k.h {
				sum += float64(pg.k.at(x, y))
			}
		}
		return sum / float64(len(px))
	}
	bx, by, best := dx, dy, -1.0
	for ddy := -1; ddy <= 1; ddy++ {
		for ddx := -1; ddx <= 1; ddx++ {
			if v := inkOver(body, dx+ddx, dy+ddy); len(body) > 0 && v > best {
				bx, by, best = dx+ddx, dy+ddy, v
			}
		}
	}
	return inkOver(px, bx, by) >= 0.2
}

// cmpPos orders cells by row, then column.
func cmpPos(a, b [2]int) int {
	if a[0] != b[0] {
		return a[0] - b[0]
	}
	return a[1] - b[1]
}

// onlyAtEdge reports whether the ink in cell r, c touches only the edge of
// its line that faces line from.
func (pg *page) onlyAtEdge(r, c, from int) bool {
	strip := max(2, (pg.rows[r].b-pg.rows[r].a)/8)
	lo, hi := pg.rows[r].b-strip, pg.rows[r].b
	if from < r {
		lo, hi = pg.rows[r].a, pg.rows[r].a+strip
	}
	for y := pg.rows[r].a; y < pg.rows[r].b; y++ {
		for x := max(pg.cellX(c), 0); x < min(pg.cellX(c+1), pg.k.w); x++ {
			if pg.k.at(x, y) > inkThreshold && (y < lo || y >= hi) {
				return false
			}
		}
	}
	return true
}

// trimEdgeRows drops a first or last row less than half as tall as the
// tallest: the last pixels of a glyph from a line the selection cut, such as
// the foot of a |, which read as a stray mark.
func trimEdgeRows(rows []span) []span {
	tall := 0
	for _, r := range rows {
		tall = max(tall, r.b-r.a)
	}
	for len(rows) > 1 && 2*(rows[0].b-rows[0].a) < tall {
		rows = rows[1:]
	}
	for len(rows) > 1 && 2*(rows[len(rows)-1].b-rows[len(rows)-1].a) < tall {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// cellX is where column c starts.
func (pg *page) cellX(c int) int { return int(math.Round(pg.x0 + float64(c)*pg.pitch)) }

// Page is an image whose character grid has been found.
type Page struct{ pg *page }

// Prepare finds the character grid in img, taking milliseconds, so a caller
// can learn whether the image is monospace text before loading any fonts or
// starting another reader. It returns ErrNoText or ErrNotMonospace if not.
func Prepare(img image.Image) (*Page, error) {
	pg, ok := newPage(img)
	if !ok {
		return nil, ErrNoText
	}
	if pg.k.gridFit(pg.rows, pg.pitch, pg.x0, pg.wide) > maxBorderInk {
		return nil, ErrNotMonospace
	}
	return &Page{pg}, nil
}

// Read reads the page's text, matching it against fonts.
func (p *Page) Read(fonts []*Font) (Result, error) {
	if len(fonts) == 0 {
		return Result{}, errNoFonts
	}
	pg := p.pg
	c, rd, copies := pg.chooseFont(fonts)
	// The font is chosen on ASCII; the text is read with accented letters.
	t, labels := pg.templatesFor(c.font, c.size, true)
	rd = pg.readRows(t, c.dy, []int{c.dx - 1, c.dx, c.dx + 1})
	ops := pg.findOps(c, rd)
	// A copy of the font can only differ in its ligatures, so it wins only by
	// finding more of them on screen.
	for _, alt := range copies {
		if o := pg.findOps(alt, rd); len(o) > len(ops) {
			c, ops = alt, o
		}
	}
	wide := pg.wideGlyphs(rd, ops)
	skip := maps.Clone(ops)
	for pos := range wide {
		skip[pos], skip[[2]int{pos[0], pos[1] + 1}] = unread, unread
	}
	// The tops and bottoms of wide glyphs, drawn taller than Latin text, can
	// reach into the next line; there they are nothing.
	for pos := range wide {
		for _, r := range []int{pos[0] - 1, pos[0] + 1} {
			for _, c := range []int{pos[1], pos[1] + 1} {
				if r >= 0 && r < len(pg.occ) && c < len(pg.occ[r]) && pg.occ[r][c] && pg.onlyAtEdge(r, c, pos[0]) {
					pg.occ[r][c] = false
					skip[[2]int{r, c}] = unread
				}
			}
		}
	}
	dxr := []int{c.dx - 1, c.dx, c.dx + 1}
	t2, labels2 := pg.learn(t, labels, rd, skip, dxr)
	if len(t2) > len(t) {
		rd = pg.readRows(t2, c.dy, dxr)
		labels = labels2
	}
	chars := map[[2]int]rune{}
	for id, pos := range pg.where {
		chars[pos] = labels[rd.picks[id]]
	}
	pg.dropMissingMarks(c.font, c.size, chars, rd, c.dx)
	for pos, ch := range ops {
		chars[pos] = ch
	}
	base := math.MaxInt
	for _, row := range pg.occ {
		for c, on := range row {
			if on {
				base = min(base, c)
				break
			}
		}
	}
	var lines []string
	var runs []Wide
	for r, row := range pg.occ {
		last := -1
		for c, on := range row {
			if on {
				last = c
			}
		}
		var b []rune
		for c := base; c <= last; c++ {
			switch {
			case wide[[2]int{r, c}]:
				cell := image.Rect(pg.cellX(c), pg.rows[r].a, pg.cellX(c+2), pg.rows[r].b)
				if n := len(runs) - 1; n >= 0 && runs[n].Line == len(lines) && runs[n].Start+runs[n].Count == len(b) {
					runs[n].Count++
					runs[n].Bounds = runs[n].Bounds.Union(cell)
				} else {
					runs = append(runs, Wide{Line: len(lines), Start: len(b), Count: 1, Bounds: cell})
				}
				b = append(b, unread)
				c++
			case row[c]:
				b = append(b, chars[[2]int{r, c}])
			default:
				b = append(b, ' ')
			}
		}
		lines = append(lines, string(b))
	}
	lead := 0
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
		lead++
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range runs {
		runs[i].Line -= lead
	}
	var read []float32
	for id, s := range rd.scores {
		if skip[pg.where[id]] != unread {
			read = append(read, s)
		}
	}
	return Result{Text: strings.Join(lines, "\n"), Font: c.font.Name, Score: mean32(read), Wide: runs}, nil
}
