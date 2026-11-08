package cellread

import (
	"errors"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// goMono is the one candidate font in these tests, so they don't depend on
// what is installed. It ships with golang.org/x/image.
func goMono(t *testing.T) []*Font {
	t.Helper()
	f, err := LoadFonts("gomono.ttf", gomono.TTF)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

type theme struct {
	bg     color.Color
	colors []color.Color // cycled per word, like syntax highlighting
}

var (
	dark  = theme{color.RGBA{0x27, 0x2a, 0x30, 0xff}, []color.Color{color.RGBA{0xd4, 0xd4, 0xd4, 0xff}, color.RGBA{0xc6, 0x78, 0xdd, 0xff}, color.RGBA{0x98, 0xc3, 0x79, 0xff}}}
	light = theme{color.RGBA{0xf6, 0xf8, 0xfa, 0xff}, []color.Color{color.RGBA{0x1f, 0x23, 0x28, 0xff}, color.RGBA{0xcf, 0x22, 0x2e, 0xff}}}
)

// screenshot draws text the way a terminal would, through x/image's own
// text drawing rather than this package's glyph code.
func screenshot(t *testing.T, text string, th theme, size float64) image.Image {
	t.Helper()
	return drawText(t, gomono.TTF, text, th, size)
}

func drawText(t *testing.T, ttf []byte, text string, th theme, size float64) image.Image {
	t.Helper()
	parsed, err := opentype.Parse(ttf)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(text, "\n")
	pitch := int(size * 1.4)
	widest := 0
	for _, l := range lines {
		widest = max(widest, len(l))
	}
	img := image.NewRGBA(image.Rect(0, 0, int(float64(widest)*size*0.6)+40, pitch*len(lines)+24))
	draw.Draw(img, img.Bounds(), image.NewUniform(th.bg), image.Point{}, draw.Src)
	for i, l := range lines {
		d := &font.Drawer{Dst: img, Face: face, Dot: fixed.P(16, 12+int(size)+i*pitch)}
		for j, word := range strings.SplitAfter(l, " ") {
			d.Src = image.NewUniform(th.colors[j%len(th.colors)])
			d.DrawString(word)
		}
	}
	return img
}

func TestReadsCodeExactly(t *testing.T) {
	cases := []struct {
		name string
		th   theme
		size float64
		text string
	}{
		{"indentation and lookalikes, dark", dark, 16, "func parse(l1 int) (int, error) {\n    if l1 == 0 {\n        return O0, nil\n    }\n    return lI1, fmt.Errorf(\"%w\", err)\n}"},
		{"symbols, light", light, 16, "x := []int{1, 2}\nm[\"k\"] = `v` + 'c'\nif a != b && c <= d || !e {\n  y |= z ^ ~w; _ = $@#%"},
		// Short and long lines alternate, so the ink repeats more strongly
		// every two lines than every one.
		{"alternating line lengths", dark, 16, "if ok {\n    return handleTheRequestWithAllOfItsOptions(r)\n}\nif !ok {\n    return handleTheRequestWithAllOfItsOptions(w)\n}"},
		{"one line", dark, 16, "frame extract -l deu /tmp/shot.png"},
		{"blank line kept", light, 16, "a = 1\n\nb = 2"},
		{"small text", dark, 12, "for i := 0; i < n; i++ {\n\tsum += xs[i]\n}"},
	}
	fonts := goMono(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := strings.ReplaceAll(c.text, "\t", "    ")
			img := screenshot(t, want, c.th, c.size)
			got, err := Read(img, fonts)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != want {
				t.Errorf("read\n%s\nwant\n%s", got.Text, want)
			}
			if got.Font != "gomono.ttf" {
				t.Errorf("font %q, want gomono.ttf", got.Font)
			}
		})
	}
}

func TestBlankImageIsNoText(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 80))
	draw.Draw(img, img.Bounds(), image.NewUniform(dark.bg), image.Point{}, draw.Src)
	if _, err := Read(img, goMono(t)); !errors.Is(err, ErrNoText) {
		t.Fatalf("err %v, want ErrNoText", err)
	}
}

func TestThinRuleIsNoText(t *testing.T) {
	// A selection of a divider line: too thin to hold a glyph.
	img := image.NewRGBA(image.Rect(0, 0, 200, 40))
	draw.Draw(img, img.Bounds(), image.NewUniform(dark.bg), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(10, 19, 190, 21), image.NewUniform(dark.colors[0]), image.Point{}, draw.Src)
	if _, err := Read(img, goMono(t)); !errors.Is(err, ErrNoText) {
		t.Fatalf("err %v, want ErrNoText", err)
	}
}

func TestNoFontsIsAnError(t *testing.T) {
	img := screenshot(t, "x", dark, 16)
	if _, err := Read(img, nil); !errors.Is(err, errNoFonts) {
		t.Fatalf("err %v, want errNoFonts", err)
	}
}

func TestLoadFontRejectsProportional(t *testing.T) {
	// Go Regular is proportional: i and M differ in width.
	if _, err := LoadFonts("goregular.ttf", goregular.TTF); err == nil {
		t.Fatal("loaded a proportional font as monospace")
	}
}

func TestProportionalTextIsNotMonospace(t *testing.T) {
	img := drawText(t, goregular.TTF, "ebb4ca35-9058-4234-b301-83e86314f4cd\nWill you be around on Thursday?", dark, 16)
	if res, err := Read(img, goMono(t)); !errors.Is(err, ErrNotMonospace) {
		t.Fatalf("read %q, err %v, want ErrNotMonospace", res.Text, err)
	}
}

func TestClippedNeighbourLinesAreIgnored(t *testing.T) {
	// A selection around one line catches the bottom of the line above and
	// the top of the line below. At size 16 baselines fall at 28, 50 and 72.
	img := screenshot(t, "gyp jog quay\nif err != nil {\nHELLO WORLD", dark, 16)
	area := img.(*image.RGBA).SubImage(image.Rect(0, 29, img.Bounds().Dx(), 63))
	got, err := Read(area, goMono(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := "if err != nil {"; got.Text != want {
		t.Fatalf("read %q, want %q", got.Text, want)
	}
}

func TestReadsBoldText(t *testing.T) {
	bold, err := LoadFonts("gomonobold.ttf", gomonobold.TTF)
	if err != nil {
		t.Fatal(err)
	}
	want := "drag over it, print it\nl1 O0 = {quit}"
	img := drawText(t, gomonobold.TTF, want, dark, 16)
	got, err := Read(img, append(goMono(t), bold...))
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != want || got.Font != "gomonobold.ttf" {
		t.Fatalf("read %q in %s, want %q in gomonobold.ttf", got.Text, got.Font, want)
	}
}
