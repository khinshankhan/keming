package cellread

import (
	"bytes"
	"errors"
	"image"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
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
	Name    string // file name, for reporting which font was picked
	family  string // normalised, so a family's weights and Nerd Font builds group together
	weight  font.Weight
	face    *font.Face
	advance float64 // cell width per pixel of font size
}

var errNoFonts = errors.New("no monospace fonts found")

// fontDirs are where fonts are installed, on Linux and macOS.
func fontDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/usr/share/fonts", "/usr/local/share/fonts", "/System/Library/Fonts", "/Library/Fonts"}
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

// InstalledFonts finds the upright, roughly regular weight monospace fonts
// installed on this machine. Text drawn on this screen used one of them, so
// they are the candidates for reading it back. Nothing is downloaded.
func InstalledFonts() ([]*Font, error) {
	seen := map[string]bool{}
	var out []*Font
	for _, dir := range fontDirs() {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".ttf" && ext != ".otf" {
				return nil
			}
			real, err := filepath.EvalSymlinks(path)
			if err != nil || seen[real] {
				return nil
			}
			seen[real] = true
			if f := loadFont(real); f != nil {
				out = append(out, f)
			}
			return nil
		})
	}
	if len(out) == 0 {
		return nil, errNoFonts
	}
	return out, nil
}

// loadFont returns nil for anything that can't sit on a monospace grid.
func loadFont(path string) *Font {
	// Proportional Nerd Font builds keep a mono family name but vary widths.
	if strings.Contains(strings.ToLower(filepath.Base(path)), "propo") || !fixedPitch(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	face, err := font.ParseTTF(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return newFont(filepath.Base(path), face)
}

// fixedPitch reads only the post table's isFixedPitch flag, so checking every
// installed font, some of them tens of megabytes, stays cheap.
func fixedPitch(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	ld, err := ot.NewLoader(f)
	if err != nil {
		return false
	}
	post, err := ld.RawTable(ot.MustNewTag("post"))
	if err != nil || len(post) < 16 {
		return false
	}
	return post[12]|post[13]|post[14]|post[15] != 0
}

var familySuffix = regexp.MustCompile(`(nerdfontmono|nerdfontpropo|nerdfont|nfm|nfp|nf)$`)

func newFont(name string, face *font.Face) *Font {
	desc := face.Describe()
	if desc.Aspect.Style != font.StyleNormal || desc.Aspect.Weight < 350 || desc.Aspect.Weight > 650 {
		return nil
	}
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
	return &Font{
		Name:    name,
		family:  familySuffix.ReplaceAllString(fam, ""),
		weight:  desc.Aspect.Weight,
		face:    face,
		advance: float64(am) / float64(face.Upem()),
	}
}

// LoadFont reads one font file, for callers that know the font in advance.
func LoadFont(name string, data []byte) (*Font, error) {
	face, err := font.ParseTTF(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	f := newFont(name, face)
	if f == nil {
		return nil, errors.New("not an upright regular monospace font")
	}
	return f, nil
}

// glyph draws one outline into a w x h cell: pen at x, baseline at y, size in
// pixels per em. The rasterizer computes exact pixel coverage, which stands in
// for any renderer's antialiasing.
func (f *Font) glyph(gid font.GID, size, x, y float64, snap hinting, z *vector.Rasterizer) {
	outline, ok := f.face.GlyphDataOutline(gid)
	if !ok {
		return
	}
	s := float32(size / float64(f.face.Upem()))
	lift := func(v float32) float32 { return v * s }
	if snap != unhinted {
		lift = f.snapHeights(s, snap == compactHinted)
	}
	px := func(p ot.SegmentPoint) (float32, float32) { return float32(x) + p.X*s, float32(y) - lift(p.Y) }
	for _, seg := range outline.Segments {
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
	// Heights in font units, from the bottom of p to the top of l or d.
	marks := []float32{f.extreme(-1, 'p', 'g'), 0, f.face.LineMetric(font.XHeight), f.face.LineMetric(font.CapHeight), f.extreme(1, 'l', 'd')}
	from := make([]float32, 0, len(marks))
	for _, m := range marks {
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
func (f *Font) extreme(dir float32, chars ...rune) float32 {
	var out float32
	for _, ch := range chars {
		gid, ok := f.face.NominalGlyph(ch)
		if !ok {
			continue
		}
		e, ok := f.face.GlyphExtents(gid)
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

// templates draws every printable ASCII character the font has, at each
// sub-pixel offset, as unit vectors for matching.
func (f *Font) templates(size float64, w, h int, baseline float64, phases []float64) ([][]float32, []rune) {
	var vecs [][]float32
	var labels []rune
	z := vector.NewRasterizer(w, h)
	for ch := rune(33); ch < 127; ch++ {
		gid, ok := f.face.NominalGlyph(ch)
		if !ok {
			continue
		}
		for _, ph := range phases {
			z.Reset(w, h)
			f.glyph(gid, size, ph, baseline, unhinted, z)
			vecs = append(vecs, unit(coverage(z, w, h)))
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
				vecs = append(vecs, unit(coverage(z, w, h)))
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

// operatorTemplates draws each operator with the font's own shaping, which is
// where ligatures come from. Fonts without ligatures draw plain characters,
// which then never beat reading the cells one by one.
func (f *Font) operatorTemplates(size, pitch float64, h int, baseline float64) map[string][]float32 {
	shaper := &shaping.HarfbuzzShaper{}
	out := map[string][]float32{}
	for _, op := range operators {
		text := []rune(op)
		res := shaper.Shape(shaping.Input{
			Text: text, RunStart: 0, RunEnd: len(text), Direction: di.DirectionLTR, Face: f.face,
			Size: fixed.Int26_6(math.Round(size * 64)), Script: language.Latin, Language: language.NewLanguage("en"),
		})
		w := int(math.Round(pitch * float64(len(text))))
		z := vector.NewRasterizer(w, h)
		pen := 0.0
		for _, g := range res.Glyphs {
			f.glyph(g.GlyphID, size, pen+float64(g.XOffset)/64, baseline-float64(g.YOffset)/64, unhinted, z)
			pen += float64(g.Advance) / 64
		}
		out[op] = unit(coverage(z, w, h))
	}
	return out
}
