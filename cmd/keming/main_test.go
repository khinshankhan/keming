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
	echo '[{"text": "名探偵コナン", "box": [[0,0],[10,0],[10,5],[0,5]]}]'
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
