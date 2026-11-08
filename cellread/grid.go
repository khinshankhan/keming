package cellread

import (
	"image"
	"math"
	"sort"
)

// inkThreshold is how far from the background a pixel must be to count as
// part of a glyph when finding rows and cells.
const inkThreshold = 0.25

// ink holds how far each pixel is from the background, from 0 to 1. It
// ignores colour, so syntax highlighting and dark or light themes read alike.
type ink struct {
	w, h int
	v    []float32
}

func (k ink) at(x, y int) float32 { return k.v[y*k.w+x] }

// newInk takes the most common colour as the background.
func newInk(img image.Image) ink {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rgb := make([][3]int32, w*h)
	counts := map[[3]int32]int{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			c := [3]int32{int32(r >> 8), int32(g >> 8), int32(bl >> 8)}
			rgb[y*w+x] = c
			counts[c]++
		}
	}
	var bg [3]int32
	most := -1
	for c, n := range counts {
		if n > most || (n == most && less(c, bg)) {
			bg, most = c, n
		}
	}
	k := ink{w: w, h: h, v: make([]float32, w*h)}
	var peak float32 = 1
	for i, c := range rgb {
		d := max(abs32(c[0]-bg[0]), abs32(c[1]-bg[1]), abs32(c[2]-bg[2]))
		k.v[i] = float32(d)
		peak = max(peak, float32(d))
	}
	for i := range k.v {
		k.v[i] /= peak
	}
	return k
}

// less breaks ties between equally common colours, so the result doesn't
// depend on map order.
func less(a, b [3]int32) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// rowProfile counts glyph pixels on each row of the image.
func (k ink) rowProfile() []float64 {
	p := make([]float64, k.h)
	for y := 0; y < k.h; y++ {
		for x := 0; x < k.w; x++ {
			if k.at(x, y) > inkThreshold {
				p[y]++
			}
		}
	}
	return p
}

type span struct{ a, b int }

// bands are runs of rows with any ink. Lines that touch merge into one band,
// so bands are only a first guess at line height.
func (k ink) bands() []span {
	var out []span
	start := -1
	for y, n := range k.rowProfile() {
		switch {
		case n > 0 && start < 0:
			start = y
		case n == 0 && start >= 0:
			out = append(out, span{start, y})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, span{start, k.h})
	}
	return out
}

// eraseSlivers blanks bands at the top or bottom edge that are much shorter
// than the tallest: the cut-off bottoms and tops of the lines either side of
// a selection. They can't be read, and left in they make one line of text
// look like several, or put ink in the blank cells beside them.
func (k ink) eraseSlivers() {
	b := k.bands()
	tallest := 0
	for _, s := range b {
		tallest = max(tallest, s.b-s.a)
	}
	thin := func(s span) bool { return (s.b-s.a)*2 < tallest }
	erase := func(s span) { clear(k.v[s.a*k.w : s.b*k.w]) }
	for len(b) > 1 && b[0].a == 0 && thin(b[0]) {
		erase(b[0])
		b = b[1:]
	}
	for len(b) > 1 && b[len(b)-1].b == k.h && thin(b[len(b)-1]) {
		erase(b[len(b)-1])
		b = b[:len(b)-1]
	}
}

func medianHeight(s []span) float64 {
	hs := make([]float64, len(s))
	for i, v := range s {
		hs[i] = float64(v.b - v.a)
	}
	sort.Float64s(hs)
	return hs[len(hs)/2]
}

// linePitch measures line spacing from how the ink profile repeats. It takes
// the first lag after the profile stops resembling itself where it does again.
// The strongest repeat would be wrong: code that alternates long and short
// lines repeats far more strongly at double the spacing. The estimate is then
// refined on multiples of itself for sub-pixel accuracy.
func linePitch(prof []float64, lo0, hi float64) float64 {
	n := len(prof)
	mean := 0.0
	for _, v := range prof {
		mean += v
	}
	mean /= float64(n)
	p := make([]float64, n)
	for i, v := range prof {
		p[i] = v - mean
	}
	ac := make([]float64, n)
	for lag := 0; lag < n; lag++ {
		s := 0.0
		for i := 0; i+lag < n; i++ {
			s += p[i] * p[i+lag]
		}
		ac[lag] = s / float64(n-lag)
	}
	top := min(int(hi), n-2)
	lo := 2
	for lag := 1; lag < n; lag++ {
		if ac[lag] <= 0 {
			lo = lag
			break
		}
	}
	// Ink inside one line can repeat too, between x-height and ascenders, but
	// lines can't sit closer together than the shortest line is tall.
	lo = max(lo, int(lo0))
	pitch := -1.0
	for lag := lo + 1; lag < top; lag++ {
		if ac[lag] > 0 && ac[lag] >= ac[lag-1] && ac[lag] >= ac[lag+1] {
			pitch = float64(lag)
			break
		}
	}
	if pitch < 0 {
		best := lo
		for lag := lo; lag <= top && lag < n; lag++ {
			if ac[lag] > ac[best] {
				best = lag
			}
		}
		pitch = float64(best)
	}
	for k := 2; float64(k)*pitch+3 < float64(n)*0.75; k++ {
		c := int(math.Round(float64(k) * pitch))
		q := c - 1
		for j := c; j <= c+1; j++ {
			if ac[j] > ac[q] {
				q = j
			}
		}
		pitch = float64(q) / float64(k)
	}
	return pitch
}

// lineGrid fits evenly spaced text rows, since tall glyphs can touch the next
// line and hide the gap between them. It returns every row on the grid,
// blank ones included.
func (k ink) lineGrid() []span {
	bands := k.bands()
	if len(bands) == 0 {
		return nil
	}
	top, bottom := bands[0].a, bands[len(bands)-1].b-1
	tall := medianHeight(bands)
	if len(bands) == 1 {
		// One line has nothing to repeat, so pad the band to give glyphs room.
		pad := int(math.Ceil(tall * 0.25))
		return []span{{max(top-pad, 0), min(bottom+1+pad, k.h)}}
	}
	prof := k.rowProfile()
	shortest := tall
	for _, b := range bands {
		shortest = min(shortest, float64(b.b-b.a))
	}
	pitch := linePitch(prof, shortest*0.8, tall*2.2)
	if len(bands) <= 3 {
		// Too few lines for their ink to repeat, so measure between them.
		if p, ok := gapPitch(bands, tall); ok {
			pitch = p
		}
	}
	n := int(float64(bottom-top)/pitch) + 2
	var clean []float64
	best := math.Inf(1)
	for phase := 0.0; phase < pitch; phase += 0.25 {
		cost := 0.0
		for r := 0; r < n; r++ {
			y := int(math.Round(float64(top) - phase + float64(r)*pitch))
			if y >= 0 && y < len(prof) {
				cost += prof[y]
			}
		}
		switch {
		case cost < best:
			best, clean = cost, []float64{phase}
		case cost == best:
			clean = append(clean, phase)
		}
	}
	// The middle of the clean offsets sits furthest from the glyphs either side.
	y0 := float64(top) - clean[len(clean)/2]
	rows := int((float64(bottom)-y0)/pitch) + 1
	out := make([]span, 0, rows)
	for r := 0; r < rows; r++ {
		a := max(int(math.Round(y0+float64(r)*pitch)), 0)
		b := min(int(math.Round(y0+float64(r+1)*pitch)), k.h)
		out = append(out, span{a, b})
	}
	return out
}

// gapPitch measures line spacing from where a few separate lines start. A
// blank line between two doubles the distance, so each distance is split
// into the fewest lines that leave a plausible spacing: from a little more
// than a line's height to just over twice it.
func gapPitch(bands []span, tall float64) (float64, bool) {
	if len(bands) < 2 {
		return 0, false
	}
	var sum float64
	var lines int
	for i := 1; i < len(bands); i++ {
		d := float64(bands[i].a - bands[i-1].a)
		k := 1
		for d/float64(k) > tall*2.2 {
			k++
		}
		if d/float64(k) < tall*1.05 {
			return 0, false
		}
		sum += d
		lines += k
	}
	return sum / float64(lines), true
}

// cellPitch finds the cell width and the left edge of the first cell: the
// grid whose cell borders cross the least ink.
func (k ink) cellPitch(bands []span) (pitch, x0 float64, ok bool) {
	col := make([]float64, k.w)
	for _, s := range bands {
		for y := s.a; y < s.b; y++ {
			for x := 0; x < k.w; x++ {
				if k.at(x, y) > inkThreshold {
					col[x]++
				}
			}
		}
	}
	first, last := -1, -1
	for x, n := range col {
		if n > 0 {
			if first < 0 {
				first = x
			}
			last = x
		}
	}
	if first < 0 {
		return 0, 0, false
	}
	tall := medianHeight(bands)
	bestKey := math.Inf(1)
	// Cells narrower than 4 pixels aren't legible, and ones wider than a line
	// is tall aren't plausible text.
	for p := math.Max(4, tall*0.35); p < tall*1.1; p += 0.01 {
		n := int(float64(last-first)/p) + 2
		for phase := 0.0; phase < p; phase += 0.1 {
			cost, inside := 0.0, 0
			for i := 0; i < n; i++ {
				x := int(math.Round(float64(first) - phase + float64(i)*p))
				if x >= 0 && x < k.w {
					cost += col[x]
					inside++
				}
			}
			// Among equally clean fits the widest wins: half a cell is just as clean.
			key := cost/float64(max(inside, 1)) - p*1e-3
			if key < bestKey {
				bestKey, pitch, x0 = key, p, float64(first)-phase
			}
		}
	}
	return pitch, x0, pitch > 0
}

// occupied reports, per row, which cells hold a glyph. Only the middle of a
// cell counts, so a neighbour's overhang isn't taken for a glyph.
func (k ink) occupied(rows []span, pitch, x0 float64) [][]bool {
	ncol := int((float64(k.w)-x0)/pitch) + 1
	out := make([][]bool, len(rows))
	for r, s := range rows {
		out[r] = make([]bool, ncol)
		for c := 0; c < ncol; c++ {
			l := int(math.Round(x0 + (float64(c)+0.2)*pitch))
			rr := max(int(math.Round(x0+(float64(c)+0.8)*pitch)), l+1)
			l, rr = max(l, 0), min(rr, k.w)
		scan:
			for y := s.a; y < s.b; y++ {
				for x := l; x < rr; x++ {
					if k.at(x, y) > inkThreshold {
						out[r][c] = true
						break scan
					}
				}
			}
		}
	}
	return out
}

// gridFit is how much ink the cell borders cross, relative to an average
// column of the text: near 0 for monospace text, which keeps to its cells,
// and well above for proportional text, whose glyphs fall anywhere.
func (k ink) gridFit(rows []span, pitch, x0 float64) float64 {
	var border, all float64
	var nb, na int
	for _, s := range rows {
		first, last := k.w, -1
		col := make([]float64, k.w)
		for y := s.a; y < s.b; y++ {
			for x := 0; x < k.w; x++ {
				if k.at(x, y) > inkThreshold {
					col[x]++
					first, last = min(first, x), max(last, x)
				}
			}
		}
		if last < 0 {
			continue
		}
		for x := first; x <= last; x++ {
			all += col[x]
			na++
		}
		c0 := int(math.Ceil((float64(first) - x0) / pitch))
		for c := c0; ; c++ {
			x := int(math.Round(x0 + float64(c)*pitch))
			if x > last {
				break
			}
			border += col[x]
			nb++
		}
	}
	if nb == 0 || all == 0 {
		return 1
	}
	return (border / float64(nb)) / (all / float64(na))
}
