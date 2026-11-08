//go:build unix

package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khinshankhan/keming/cellread"
	"golang.org/x/image/draw"
)

func TestChoose(t *testing.T) {
	fail := errors.New("tesseract failed")
	noText := errors.New("no text found")
	cells := func(text string, score float64) cellReading {
		return cellReading{res: cellread.Result{Text: text, Font: "Mono.ttf", Score: score}}
	}
	other := func(text string, err error) textReading { return textReading{"tesseract", text, err} }
	tests := []struct {
		name    string
		cells   cellReading
		other   textReading
		want    string
		wantErr error
	}{
		{"confident cell reader wins", cells("if l1 == 0 {", 0.98), other("if 11 == 0 {", nil), "if l1 == 0 {", nil},
		{"right at the cutoff wins", cells("x := 1", 0.9), other("x = 1", nil), "x := 1", nil},
		{"unsure cell reader loses", cells("itt4caJ5MKEKP", 0.69), other("ebb4ca35-9058", nil), "ebb4ca35-9058", nil},
		{"no monospace text found", cellReading{err: cellread.ErrNotMonospace}, other("hello", nil), "hello", nil},
		{"confident cell reader covers a failure", cells("ok", 0.97), other("", fail), "ok", nil},
		{"an unsure guess is no answer", cells("x#q", 0.5), other("", noText), "", noText},
		{"the other reader's error comes through", cells("x#q", 0.5), other("", fail), "", fail},
		// A title in Chinese or Japanese is all wide characters, which the
		// other reader reads better whole.
		{"mostly wide characters lose", cellReading{res: cellread.Result{Text: "\uFFFD\uFFFD\uFFFD0", Score: 0.99, Wide: []cellread.Wide{{Count: 3}}}}, other("名探偵0", nil), "名探偵0", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := choose(tt.cells, tt.other)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got error %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPickEngine(t *testing.T) {
	both := map[string]bool{"tesseract": true, "keming-rapidocr": true}
	onlyTess := map[string]bool{"tesseract": true}
	none := map[string]bool{}
	tests := []struct {
		engine, lang string
		installed    map[string]bool
		want         string
		wantErr      string
	}{
		{"auto", "eng", both, "PP-OCRv6", ""},
		{"auto", "eng", onlyTess, "tesseract", ""},
		{"auto", "deu", both, "tesseract", ""},
		{"auto", "eng", none, "", "needs tesseract installed"},
		{"rapid", "eng", onlyTess, "", "rapidocr/README.md"},
		{"tesseract", "eng", both, "tesseract", ""},
		{"cells", "eng", none, "", ""},
		{"paddle", "eng", both, "", "unknown engine"},
	}
	for _, tt := range tests {
		t.Run(tt.engine+" "+tt.lang, func(t *testing.T) {
			look := func(bin string) (string, error) {
				if tt.installed[bin] {
					return "/bin/" + bin, nil
				}
				return "", errors.New("not found")
			}
			got, _, err := pickEngine(tt.engine, tt.lang, look)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got error %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

// pngOf encodes a small image, as a caller would send it.
func pngOf(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return &b
}

func TestRunWithTesseract(t *testing.T) {
	dir := t.TempDir()
	// Saves what it was sent, so the test can check the image came through.
	script := "#!/bin/sh\ncat > " + filepath.Join(dir, "sent.png") + "\nprintf 'hello\\n\\f'\n"
	if err := os.WriteFile(filepath.Join(dir, "tesseract"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errOut bytes.Buffer
	if err := run([]string{"-engine", "tesseract", "-v"}, pngOf(t), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello\n" {
		t.Fatalf("printed %q, want %q", out.String(), "hello\n")
	}
	if !strings.Contains(errOut.String(), "read by tesseract") {
		t.Fatalf("-v said %q", errOut.String())
	}
	f, err := os.Open(filepath.Join(dir, "sent.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sent, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Bounds().Dx() != 6 || sent.Bounds().Dy() != 4 {
		t.Fatalf("tesseract got a %v image, want the 3x2 input at 2x", sent.Bounds())
	}
}

func TestRetryWithAMarginWhenPPOCRFindsNothing(t *testing.T) {
	dir := t.TempDir()
	// Finds nothing in the first image it is sent and reads the second,
	// saving both, as PP-OCRv6 did with a title that ran to the edge.
	script := `#!/bin/sh
cd ` + dir + `
if [ -e sent-1.png ]; then
	cat > sent-2.png
	echo '[{"text": "名探偵コナン", "box": [[33,33],[37,33],[37,35],[33,35]]}]'
else
	cat > sent-1.png
	echo '[]'
fi
`
	if err := os.WriteFile(filepath.Join(dir, "keming-rapidocr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errOut bytes.Buffer
	if err := run([]string{"-engine", "rapid"}, pngOf(t), &out, &errOut); err != nil {
		t.Fatalf("%v: %s", err, errOut.String())
	}
	if out.String() != "名探偵コナン\n" {
		t.Fatalf("printed %q, want the second reading", out.String())
	}
	// 3x2 at 2x is 6x4; the retry adds 32 pixels on every side.
	for name, want := range map[string]image.Rectangle{
		"sent-1.png": image.Rect(0, 0, 6, 4),
		"sent-2.png": image.Rect(0, 0, 70, 68),
	} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds() != want {
			t.Errorf("%s is %v, want %v", name, img.Bounds(), want)
		}
	}
}

func TestKoreanFallsBackToTesseract(t *testing.T) {
	tests := []struct {
		name, tesseractSays, want, wantErr string
	}{
		{"Korean is kept", "권왕환생", "권왕환생\n", ""},
		// A picture with no text should say so, not print what tesseract
		// made of it.
		{"anything else is no text", "ZEA", "", "no text found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fakes := map[string]string{
				// PP-OCRv6's model has no Hangul, so it finds nothing.
				"keming-rapidocr": "#!/bin/sh\ncat > /dev/null\necho '[]'\n",
				"tesseract":       "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "args") + "\ncat > /dev/null\nprintf '" + tt.tesseractSays + "\\n\\f'\n",
			}
			for name, script := range fakes {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			var out, errOut bytes.Buffer
			err := run([]string{"-engine", "rapid"}, pngOf(t), &out, &errOut)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Fatalf("printed %q, want %q", out.String(), tt.want)
			}
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), "-l kor") {
				t.Fatalf("tesseract ran with %q, want -l kor", args)
			}
		})
	}
}

func TestEmptyStdinIsQuiet(t *testing.T) {
	// A caller that started keming early sends nothing when the user cancels.
	t.Setenv("PATH", t.TempDir())
	var out, errOut bytes.Buffer
	if err := run([]string{"-engine", "cells"}, &bytes.Buffer{}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("printed %q and %q, want nothing", out.String(), errOut.String())
	}
}

func TestNotAnImage(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := run([]string{"-engine", "cells"}, strings.NewReader("not a png"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "reading the image") {
		t.Fatalf("got %v, want an image error", err)
	}
}

func TestCropAndUpscale(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	src := image.NewRGBA(image.Rect(0, 0, 6, 6))
	for y := range 6 {
		for x := range 6 {
			src.SetRGBA(x, y, blue)
		}
	}
	selected := image.Rect(1, 2, 3, 3)
	for y := selected.Min.Y; y < selected.Max.Y; y++ {
		for x := selected.Min.X; x < selected.Max.X; x++ {
			src.SetRGBA(x, y, red)
		}
	}

	got := upscale(src.SubImage(selected), 3)

	if got.Bounds() != image.Rect(0, 0, 6, 3) {
		t.Fatalf("got bounds %v, want a 6x3 image at the origin", got.Bounds())
	}
	// Any blue means pixels from outside the selection leaked in.
	for y := range 3 {
		for x := range 6 {
			if got.RGBAAt(x, y) != red {
				t.Errorf("pixel %d,%d: got %v, want the selection's red", x, y, got.RGBAAt(x, y))
			}
		}
	}
}

func TestPaddedUsesTheEdgeColour(t *testing.T) {
	navy := color.RGBA{21, 31, 46, 255}
	white := color.RGBA{255, 255, 255, 255}
	// A navy image with white text touching its right edge, as in a tight
	// selection.
	src := image.NewRGBA(image.Rect(10, 10, 20, 15))
	for y := 10; y < 15; y++ {
		for x := 10; x < 20; x++ {
			src.SetRGBA(x, y, navy)
		}
	}
	src.SetRGBA(19, 12, white)

	got := padded(src, 3)

	if got.Bounds() != image.Rect(0, 0, 16, 11) {
		t.Fatalf("got bounds %v, want 16x11", got.Bounds())
	}
	for _, p := range []image.Point{{0, 0}, {15, 10}, {2, 5}, {15, 5}} {
		if got.RGBAAt(p.X, p.Y) != navy {
			t.Errorf("margin at %v is %v, want the navy background", p, got.RGBAAt(p.X, p.Y))
		}
	}
	if got.RGBAAt(12, 5) != white {
		t.Errorf("the text pixel moved: %v at 12,5", got.RGBAAt(12, 5))
	}
}

func TestFillWide(t *testing.T) {
	// No tesseract, so a run PP-OCRv6 can't read stays unread.
	t.Setenv("PATH", t.TempDir())
	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 255, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	img := image.NewRGBA(image.Rect(0, 0, 200, 60))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	runs := []cellread.Wide{
		{Line: 0, Start: 3, Count: 2, Bounds: image.Rect(30, 0, 70, 20)},
		{Line: 0, Start: 8, Count: 1, Bounds: image.Rect(110, 0, 130, 20)},
		{Line: 2, Start: 0, Count: 1, Bounds: image.Rect(0, 40, 20, 60)},
	}
	for i, c := range []color.Color{red, green, blue} {
		draw.Draw(img, runs[i].Bounds.Inset(4), image.NewUniform(c), image.Point{}, draw.Src)
	}
	res := cellread.Result{Text: "// \uFFFD\uFFFD = \uFFFD;\nx\n\uFFFD", Wide: runs}
	// Reads each band as the colour it finds there; blue can't be read.
	names := map[color.RGBA]string{red: "世界", green: "注"}
	bands := func(stack image.Image, rects []image.Rectangle) ([]string, error) {
		out := make([]string, len(rects))
		for i, r := range rects {
			found := map[string]bool{}
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					c := color.RGBAModel.Convert(stack.At(x, y)).(color.RGBA)
					if n, ok := names[c]; ok && !found[n] {
						found[n] = true
						out[i] += n
					}
				}
			}
		}
		return out, nil
	}

	got, left := fillWide(img, res, bands, readKorean)

	if want := "// 世界 = 注;\nx\n\uFFFD"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if left != 1 {
		t.Fatalf("%d left unread, want 1", left)
	}
}
