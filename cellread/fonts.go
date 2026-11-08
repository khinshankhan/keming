package cellread

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Font is one installed monospace face that text on screen might be drawn in.
type Font struct {
	Name string // file name, for reporting which font was picked, with #n for a collection's nth font and @weight for a variable font's instance
	fontData
}

// fontData is all that reading needs from a font file: about a hundred glyph
// outlines, a few heights and the operators as the font shapes them. A parsed
// font file holds far more, up to tens of megabytes for a Nerd Font, so only
// this is kept, and cached between runs.
type fontData struct {
	Family  string // normalised, so a family's weights and Nerd Font builds group together
	Weight  float32
	Advance float64 // cell width per pixel of font size
	Upem    float32
	// Marks are the heights snapHeights rounds, in font units: the bottom of
	// p or g, the baseline, x-height, cap height and the top of l or d.
	Marks    []float32
	Glyphs   map[rune]font.GID // ascii and latin
	Outlines map[font.GID][]ot.Segment
	Ops      map[string][]placed // operators shaped by the font
}

// placed is one glyph of a shaped operator, in font units.
type placed struct {
	GID           font.GID
	X, Y, Advance float32
}

var errNoFonts = errors.New("no monospace fonts found")

// fontDirs are where fonts are installed, on Linux and macOS.
func fontDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/usr/share/fonts", "/usr/local/share/fonts", "/System/Library/Fonts", "/Library/Fonts",
		// SF Mono, Terminal's default on macOS, ships inside Terminal.
		"/System/Applications/Utilities/Terminal.app/Contents/Resources/Fonts"}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local/share/fonts"), filepath.Join(home, ".fonts"), filepath.Join(home, "Library/Fonts"))
	}
	for _, d := range filepath.SplitList(os.Getenv("XDG_DATA_DIRS")) {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, "fonts"))
		}
	}
	return dirs
}

// InstalledFonts finds the upright monospace fonts, regular to bold,
// installed on this machine. Text drawn on this screen used one of them, so
// they are the candidates for reading it back. Nothing is downloaded.
//
// What each font file yields is cached in the user cache directory, keyed by
// path, size and modification time, so later runs parse only new or changed
// files. Without a cache directory every file is parsed, one at a time.
func InstalledFonts() ([]*Font, error) {
	cache, cachePath := loadFontCache()
	fresh := fontCache{}
	changed := false
	seen := map[string]bool{}
	var out []*Font
	for _, dir := range fontDirs() {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ttf", ".otf", ".ttc", ".otc":
			default:
				return nil
			}
			real, err := filepath.EvalSymlinks(path)
			if err != nil || seen[real] {
				return nil
			}
			seen[real] = true
			info, err := os.Stat(real)
			if err != nil {
				return nil
			}
			e, ok := cache[real]
			if !ok || e.Size != info.Size() || e.ModTime != info.ModTime().UnixNano() {
				e = cacheEntry{Size: info.Size(), ModTime: info.ModTime().UnixNano(), Fonts: loadFonts(real)}
				changed = true
			}
			fresh[real] = e
			for _, f := range e.Fonts {
				out = append(out, &Font{Name: f.Name, fontData: *f.Data})
			}
			return nil
		})
	}
	if changed || len(fresh) != len(cache) {
		saveFontCache(cachePath, fresh)
	}
	if len(out) == 0 {
		return nil, errNoFonts
	}
	return out, nil
}

// loadFonts returns the usable monospace fonts in a font file, none for
// anything that can't sit on a monospace grid.
func loadFonts(path string) []cachedFont {
	// Proportional Nerd Font builds keep a mono family name but vary widths.
	if strings.Contains(strings.ToLower(filepath.Base(path)), "propo") || !fixedPitch(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return fontsIn(filepath.Base(path), data)
}

// fontsIn returns the usable monospace fonts in one font file: each font of
// a collection, such as Menlo.ttc on macOS, and a regular and a bold from a
// variable font, such as SF Mono or JetBrains Mono's variable build.
func fontsIn(name string, data []byte) []cachedFont {
	faces, err := font.ParseTTC(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	var out []cachedFont
	for i, face := range faces {
		n := name
		if len(faces) > 1 {
			n = fmt.Sprintf("%s#%d", name, i)
		}
		desc := face.Describe()
		if desc.Aspect.Style != font.StyleNormal {
			continue
		}
		weights := variableWeights(face)
		if weights == nil {
			if desc.Aspect.Weight < 350 || desc.Aspect.Weight > 700 {
				continue
			}
			if d := extract(face, float32(desc.Aspect.Weight)); d != nil {
				out = append(out, cachedFont{n, d})
			}
			continue
		}
		for _, w := range weights {
			face.SetVariations([]font.Variation{{Tag: wght, Value: w}})
			if d := extract(face, w); d != nil {
				out = append(out, cachedFont{fmt.Sprintf("%s@%g", n, w), d})
			}
		}
	}
	return out
}

var wght = ot.MustNewTag("wght")

// variableWeights are the weights read from a variable font: regular and
// bold, or just regular if its weight axis stops short of bold. It is nil for
// a font without a weight axis.
func variableWeights(face *font.Face) []float32 {
	defer face.SetVariations(nil)
	at := func(w float32) []font.VarCoord {
		face.SetVariations([]font.Variation{{Tag: wght, Value: w}})
		return slices.Clone(face.Coords())
	}
	regular, bold := at(400), at(700)
	if len(regular) == 0 || slices.Equal(regular, at(100)) && slices.Equal(regular, bold) {
		return nil
	}
	if slices.Equal(regular, bold) {
		return []float32{400}
	}
	return []float32{400, 700}
}

// fixedPitch reads only the post table's isFixedPitch flag, so checking every
// installed font, some of them tens of megabytes, stays cheap. A collection
// is judged by its first font.
func fixedPitch(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	lds, err := ot.NewLoaders(f)
	if err != nil || len(lds) == 0 {
		return false
	}
	post, err := lds[0].RawTable(ot.MustNewTag("post"))
	if err != nil || len(post) < 16 {
		return false
	}
	return post[12]|post[13]|post[14]|post[15] != 0
}

var familySuffix = regexp.MustCompile(`(nerdfontmono|nerdfontpropo|nerdfont|nfm|nfp|nf)$`)

// extract keeps what reading needs from a parsed font of the given weight,
// or returns nil for a font that isn't monospace.
func extract(face *font.Face, weight float32) *fontData {
	desc := face.Describe()
	m, okM := face.NominalGlyph('M')
	i, okI := face.NominalGlyph('i')
	if !okM || !okI {
		return nil
	}
	am, ai := face.HorizontalAdvance(m), face.HorizontalAdvance(i)
	if am <= 0 || math.Abs(float64(am-ai)) > 0.5 {
		return nil
	}
	fam := strings.ToLower(strings.ReplaceAll(desc.Family, " ", ""))
	upem := float32(face.Upem())
	d := &fontData{
		Family:   familySuffix.ReplaceAllString(fam, ""),
		Weight:   weight,
		Advance:  float64(am) / float64(upem),
		Upem:     upem,
		Marks:    []float32{extreme(face, -1, 'p', 'g'), 0, face.LineMetric(font.XHeight), face.LineMetric(font.CapHeight), extreme(face, 1, 'l', 'd')},
		Glyphs:   map[rune]font.GID{},
		Outlines: map[font.GID][]ot.Segment{},
		Ops:      map[string][]placed{},
	}
	keep := func(gid font.GID) {
		if _, ok := d.Outlines[gid]; ok {
			return
		}
		if outline, ok := face.GlyphDataOutline(gid); ok {
			d.Outlines[gid] = slices.Clone(outline.Segments)
		} else {
			d.Outlines[gid] = nil
		}
	}
	for _, ch := range append(ascii, latinLetters...) {
		if gid, ok := face.NominalGlyph(ch); ok {
			d.Glyphs[ch] = gid
			keep(gid)
		}
	}
	// Shaped at one pixel per font unit, so positions come out in font units
	// and scale to any size.
	shaper := &shaping.HarfbuzzShaper{}
	for _, op := range operators {
		text := []rune(op)
		res := shaper.Shape(shaping.Input{
			Text: text, RunStart: 0, RunEnd: len(text), Direction: di.DirectionLTR, Face: face,
			Size: fixed.I(int(upem)), Script: language.Latin, Language: language.NewLanguage("en"),
		})
		for _, g := range res.Glyphs {
			keep(g.GlyphID)
			d.Ops[op] = append(d.Ops[op], placed{g.GlyphID, float32(g.XOffset) / 64, float32(g.YOffset) / 64, float32(g.Advance) / 64})
		}
	}
	return d
}

// LoadFonts reads one font file, for callers that know the font in advance:
// each upright, regular to bold monospace font in it.
func LoadFonts(name string, data []byte) ([]*Font, error) {
	var out []*Font
	for _, f := range fontsIn(name, data) {
		out = append(out, &Font{Name: f.Name, fontData: *f.Data})
	}
	if len(out) == 0 {
		return nil, errors.New("no upright regular to bold monospace font in " + name)
	}
	return out, nil
}

// cacheVersion changes whenever what is cached does, so old caches are
// ignored.
const cacheVersion = 4

type cacheEntry struct {
	Size, ModTime int64
	Fonts         []cachedFont // none for a file without a usable monospace font
}

// cachedFont is a Font as the cache holds it; gob skips Font's embedded
// fontData, as its type is unexported.
type cachedFont struct {
	Name string
	Data *fontData
}

type fontCache map[string]cacheEntry

type cacheFile struct {
	Version int
	Fonts   fontCache
}

// loadFontCache returns the cached fonts and where the cache lives, or an
// empty cache if there is none or it can't be read.
func loadFontCache() (fontCache, string) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return fontCache{}, ""
	}
	path := filepath.Join(dir, "keming", "fonts.gob")
	f, err := os.Open(path)
	if err != nil {
		return fontCache{}, path
	}
	defer f.Close()
	var c cacheFile
	if err := gob.NewDecoder(f).Decode(&c); err != nil || c.Version != cacheVersion || c.Fonts == nil {
		return fontCache{}, path
	}
	return c.Fonts, path
}

// saveFontCache writes the cache whole, through a temporary file, so a
// reader running at the same time sees the old cache or the new one. A cache
// that can't be written only costs the next run time.
func saveFontCache(path string, c fontCache) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "fonts-*.gob")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if err := gob.NewEncoder(tmp).Encode(cacheFile{cacheVersion, c}); err != nil {
		tmp.Close()
		return
	}
	if tmp.Close() == nil {
		os.Rename(tmp.Name(), path)
	}
}

// glyph draws one outline into a w x h cell: pen at x, baseline at y, size in
// pixels per em. The rasterizer computes exact pixel coverage, which stands in
// for any renderer's antialiasing.
func (f *Font) glyph(gid font.GID, size, x, y float64, snap hinting, z *vector.Rasterizer) {
	segments, ok := f.Outlines[gid]
	if !ok {
		return
	}
	s := float32(size) / f.Upem
	lift := func(v float32) float32 { return v * s }
	if snap != unhinted {
		lift = f.snapHeights(s, snap == compactHinted)
	}
	px := func(p ot.SegmentPoint) (float32, float32) { return float32(x) + p.X*s, float32(y) - lift(p.Y) }
	for _, seg := range segments {
		switch seg.Op {
		case ot.SegmentOpMoveTo:
			z.MoveTo(px(seg.Args[0]))
		case ot.SegmentOpLineTo:
			z.LineTo(px(seg.Args[0]))
		case ot.SegmentOpQuadTo:
			ax, ay := px(seg.Args[0])
			bx, by := px(seg.Args[1])
			z.QuadTo(ax, ay, bx, by)
		case ot.SegmentOpCubeTo:
			ax, ay := px(seg.Args[0])
			bx, by := px(seg.Args[1])
			cx, cy := px(seg.Args[2])
			z.CubeTo(ax, ay, bx, by, cx, cy)
		}
	}
	z.ClosePath()
}

// snapHeights approximates the light hinting most Linux desktops use: the
// descender, x-height, cap height and ascender land on whole pixels and the
// heights between them stretch to fit, while widths stay as drawn. Small
// hinted text differs from its raw outline mostly in these heights: at 14
// pixels Liberation Mono's l and I both hint to 9 pixels tall, though their
// outlines differ by half a pixel.
//
// The font's own hinting instructions, which pure Go can't run, sometimes pull
// the ascender down to the cap height instead. With compact set, the ascender
// rounds down, so a template exists for that as well.
func (f *Font) snapHeights(s float32, compact bool) func(float32) float32 {
	from := make([]float32, 0, len(f.Marks))
	for _, m := range f.Marks {
		v := m * s
		// A mark the font lacks, or out of order, is skipped.
		if len(from) == 0 || v > from[len(from)-1] {
			from = append(from, v)
		}
	}
	to := make([]float32, len(from))
	for i, v := range from {
		to[i] = float32(math.Round(float64(v)))
	}
	if compact && len(to) > 1 {
		to[len(to)-1] = max(float32(math.Floor(float64(from[len(from)-1]))), to[len(to)-2])
	}
	return func(v float32) float32 {
		v *= s
		if v <= from[0] {
			return v + to[0] - from[0]
		}
		for i := 1; i < len(from); i++ {
			if v <= from[i] {
				return to[i-1] + (v-from[i-1])*(to[i]-to[i-1])/(from[i]-from[i-1])
			}
		}
		return v + to[len(to)-1] - from[len(from)-1]
	}
}

// extreme is the highest top (dir 1) or lowest bottom (dir -1) of the given
// glyphs, in font units, or 0 if the font has none of them.
func extreme(face *font.Face, dir float32, chars ...rune) float32 {
	var out float32
	for _, ch := range chars {
		gid, ok := face.NominalGlyph(ch)
		if !ok {
			continue
		}
		e, ok := face.GlyphExtents(gid)
		if !ok {
			continue
		}
		v := e.YBearing
		if dir < 0 {
			v = e.YBearing + e.Height
		}
		if v*dir > out*dir {
			out = v
		}
	}
	return out
}

// hinting picks how glyph heights are drawn; see snapHeights.
type hinting int

const (
	unhinted hinting = iota
	hinted
	compactHinted
)

func coverage(z *vector.Rasterizer, w, h int) []float32 {
	dst := image.NewAlpha(image.Rect(0, 0, w, h))
	z.Draw(dst, dst.Bounds(), image.Opaque, image.Point{})
	out := make([]float32, w*h)
	for i, a := range dst.Pix {
		out[i] = float32(a)
	}
	return out
}

// subPixel are the pen offsets each glyph is drawn at, since a browser places
// glyphs at fractional positions.
var subPixel = []float64{0, 0.25, 0.5, 0.75}

// ascii is printable ASCII, apart from the space.
var ascii = func() []rune {
	var r []rune
	for ch := rune('!'); ch <= '~'; ch++ {
		r = append(r, ch)
	}
	return r
}()

// latinLetters are the accented letters of Latin-1 and Latin Extended-A,
// for the languages of western and central Europe, the Baltic and Turkey,
// with Romanian's ș and ț, Vietnamese, Spanish's ¡ and ¿, and the euro sign.
// Read as ASCII, Grüße came out as GruBe. Latin-1's symbols stay out: they
// look like ASCII marks, and the cedilla took the underscore in LOG_LEVEL.
// So do the long s, which looks like an f, and the obsolete ŉ and ĸ.
var latinLetters = func() []rune {
	r := []rune{'¡', '¿'}
	add := func(lo, hi rune) {
		for ch := lo; ch <= hi; ch++ {
			if ch != '×' && ch != '÷' && ch != 'ŉ' && ch != 'ĸ' && ch != 'ſ' {
				r = append(r, ch)
			}
		}
	}
	add(0xc0, 0xff)     // Latin-1
	add(0x100, 0x17f)   // Latin Extended-A
	add(0x218, 0x21b)   // Ș ș Ț ț
	add(0x1a0, 0x1a1)   // Ơ ơ
	add(0x1af, 0x1b0)   // Ư ư
	add(0x1ea0, 0x1ef9) // Vietnamese
	return append(r, '€')
}()

// latinWeight scales the templates of letters outside ASCII, so they score
// a little lower and only win a cell they clearly match. Unweighted, i read
// as í all through a GitHub snippet in Liberation Mono; 0.95 lost real
// accents, and 0.97 the í of Czech's Příliš in JetBrains Mono. A mark that
// isn't on screen is caught after, by dropMissingMarks.
const latinWeight = 0.98

// weigh scales v, a unit template of ch, by latinWeight when ch is outside
// ASCII.
func weigh(v []float32, ch rune) []float32 {
	if ch <= '~' {
		return v
	}
	for i := range v {
		v[i] *= latinWeight
	}
	return v
}

// templates draws every ASCII character the font has, and with latin the
// accented letters too, at each sub-pixel offset, as unit vectors for
// matching. Choosing the font uses ASCII alone: more shapes to match lift a
// wrong font more than the right one, and Adwaita Mono beat JetBrains Mono.
func (f *Font) templates(size float64, w, h int, baseline float64, phases []float64, latin bool) ([][]float32, []rune) {
	var vecs [][]float32
	var labels []rune
	z := vector.NewRasterizer(w, h)
	chars := ascii
	if latin {
		chars = append(ascii[:len(ascii):len(ascii)], latinLetters...)
	}
	for _, ch := range chars {
		gid, ok := f.Glyphs[ch]
		if !ok {
			continue
		}
		for _, ph := range phases {
			z.Reset(w, h)
			f.glyph(gid, size, ph, baseline, unhinted, z)
			vecs = append(vecs, weigh(unit(coverage(z, w, h)), ch))
			labels = append(labels, ch)
		}
		if len(phases) == 1 {
			continue // the quick font pass only needs a rough likeness
		}
		// Hinted text sits on a whole-pixel baseline as well.
		for _, hint := range []hinting{hinted, compactHinted} {
			for _, ph := range []float64{0, 0.5} {
				z.Reset(w, h)
				f.glyph(gid, size, ph, math.Round(baseline), hint, z)
				vecs = append(vecs, weigh(unit(coverage(z, w, h)), ch))
				labels = append(labels, ch)
			}
		}
	}
	return vecs, labels
}

// operators are character runs that ligature fonts draw as one shape.
var operators = []string{"->", "=>", "<-", "==", "===", "!=", "!==", ">=", "<=", "=<", "::", ":=", "++", "--", "&&", "||",
	"<<", ">>", "//", "/*", "*/", "..", "...", "<>", "|>", "<|", "#!", "www", "__", "**", "?.", "??", "!!", "%%", "#{", "${",
	"/>", "</", "~>", "<~", "=~", "!~"}

// operatorTemplates draws each operator as the font shapes it, which is
// where ligatures come from. Fonts without ligatures draw plain characters,
// which then never beat reading the cells one by one.
func (f *Font) operatorTemplates(size, pitch float64, h int, baseline float64) map[string][]float32 {
	scale := size / float64(f.Upem)
	out := map[string][]float32{}
	for _, op := range operators {
		w := int(math.Round(pitch * float64(len([]rune(op)))))
		z := vector.NewRasterizer(w, h)
		pen := 0.0
		for _, g := range f.Ops[op] {
			f.glyph(g.GID, size, pen+float64(g.X)*scale, baseline-float64(g.Y)*scale, unhinted, z)
			pen += float64(g.Advance) * scale
		}
		out[op] = unit(coverage(z, w, h))
	}
	return out
}
