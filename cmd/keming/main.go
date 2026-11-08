// Command keming reads the text in a screenshot and prints it.
//
//	keming [-l lang] [-engine name] [-v] [image]
//
// The image is a file, or PNG or JPEG on stdin. Reading stdin waits until the
// slow parts have started, loading fonts and the PP-OCRv6 models, so a caller
// such as frame can start keming before the user has picked an area and send
// the image once they have. Empty stdin means the caller gave up: keming
// exits quietly.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"unicode"

	"github.com/khinshankhan/keming/cellread"
	"github.com/khinshankhan/keming/rapid"
	"github.com/khinshankhan/keming/tesseract"
	"golang.org/x/image/draw"
)

const (
	// Screen text is small for both general readers. 2x read browser and
	// terminal text far better than the original size, and 3x was no better.
	upscaleBy = 2

	// margin is the border of background PP-OCRv6 gets on a second try when
	// it finds nothing. It found no text in a tight selection of a Japanese
	// title whose glyphs ran to the edge, and read it exactly with this
	// margin. Every image gets it only on a retry: added always, it fixed one
	// terminal line and broke two others.
	margin = 32

	// monoConfident is the cell reader's match score at which its text beats
	// the other reader's. The cell reader turns proportional text away
	// itself, so this is a backstop for text on a grid that no installed font
	// matches: code screenshots scored 0.92 and up, even with their font
	// uninstalled.
	monoConfident = 0.9
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "keming:", err)
		os.Exit(1)
	}
}

// engines are the readers -engine can name.
var engines = []string{"auto", "cells", "rapid", "tesseract"}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Reading is a burst of work on every core; whatever the user is doing
	// comes first. The PP-OCRv6 helper inherits this.
	lowerPriority()

	fs := flag.NewFlagSet("keming", flag.ContinueOnError)
	fs.SetOutput(stderr)
	lang := fs.String("l", "eng", "language, for tesseract; see tesseract --list-langs")
	engine := fs.String("engine", "auto", "reader: auto picks per image; cells reads monospace text such as code against installed fonts; rapid is PP-OCRv6 through keming-rapidocr; tesseract")
	verbose := fs.Bool("v", false, "say on stderr which reader's text was used")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: keming [-l lang] [-engine name] [-v] [image]\n\nReads the text in a screenshot, given as a file or as PNG or JPEG on stdin.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return errors.New("one image at a time")
	}

	name, binary, err := pickEngine(*engine, *lang, exec.LookPath)
	if err != nil {
		return err
	}
	useCells := *engine == "auto" || *engine == "cells"

	// The slow parts start before the image is read; see the package doc.
	fonts := make(chan loadedFonts, 1)
	if useCells {
		go func() {
			f, err := cellread.InstalledFonts()
			fonts <- loadedFonts{f, err}
		}()
	}
	var read func(image.Image) (string, error)
	// bands reads the runs of wide characters the cell reader leaves, with
	// the same helper, as only one of the two runs.
	var bands func(image.Image, []image.Rectangle) ([]string, error)
	// eachRun reads a run PP-OCRv6 didn't, or every run without it.
	eachRun := readKorean
	switch name {
	case "PP-OCRv6":
		r, err := rapid.Start(binary)
		if err != nil {
			return err
		}
		defer r.Close()
		bands = r.ReadBands
		read = func(img image.Image) (string, error) {
			big := upscale(img, upscaleBy)
			text, err := r.Read(big)
			if !errors.Is(err, rapid.ErrNoText) {
				return text, err
			}
			again, err := rapid.Start(binary)
			if err != nil {
				return "", err
			}
			defer again.Close()
			text, err = again.ReadWithin(padded(big, margin), big.Bounds().Add(image.Pt(margin, margin)))
			if !errors.Is(err, rapid.ErrNoText) {
				return text, err
			}
			return readKorean(big)
		}
	case "tesseract":
		read = func(img image.Image) (string, error) {
			return tesseract.Recognize(binary, *lang, upscale(img, upscaleBy))
		}
		if *lang != "eng" {
			eachRun = func(img image.Image) (string, error) {
				return tesseract.Recognize(binary, *lang, img)
			}
		}
	}

	img, ok, err := readImage(fs.Arg(0), stdin)
	if err != nil || !ok {
		return err
	}

	// The grid check takes milliseconds and decides which reader to run, so
	// only one runs: the cell reader on monospace text, falling back to the
	// other reader if it is unsure, and the other reader on anything else.
	var text, why string
	var c cellReading
	switch {
	case read == nil:
		c = readCells(img, fonts)
		text, err = c.res.Text, c.err
		why = fmt.Sprintf("cell reader (%s, match %.2f), as asked", c.res.Font, c.res.Score)
	case !useCells:
		text, err = read(img)
		why = name + ", as asked"
	default:
		c = readCells(img, fonts)
		if trusted(c) {
			text, why, err = choose(c, textReading{})
			break
		}
		t, terr := read(img)
		text, why, err = choose(c, textReading{name, t, terr})
	}
	if err == nil && len(c.res.Wide) > 0 && text == c.res.Text {
		var left int
		text, left = fillWide(img, c.res, bands, eachRun)
		if bands != nil {
			why += ", wide characters by PP-OCRv6"
		} else {
			why += ", wide characters by tesseract"
		}
		if left > 0 {
			why += fmt.Sprintf(", %d left unread", left)
			if bands == nil {
				why += " without " + rapid.Binary
			}
		}
	}
	if *verbose {
		fmt.Fprintln(stderr, "keming: read by", why)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, text)
	return err
}

type loadedFonts struct {
	fonts []*cellread.Font
	err   error
}

// readCells reads img with the cell reader, waiting for fonts only once the
// grid check has passed.
func readCells(img image.Image, fonts <-chan loadedFonts) cellReading {
	page, err := cellread.Prepare(img)
	if err != nil {
		return cellReading{err: err}
	}
	l := <-fonts
	if l.err != nil {
		return cellReading{err: l.err}
	}
	res, err := page.Read(l.fonts)
	return cellReading{res, err}
}

// pickEngine names the reader for text that isn't monospace and finds its
// binary: PP-OCRv6 through keming-rapidocr when it is installed, since it
// reads web pages, chat and documents best, and tesseract otherwise, or for
// languages other than English. -engine cells needs neither.
func pickEngine(engine, lang string, lookPath func(string) (string, error)) (name, binary string, err error) {
	tess := func() (string, string, error) {
		binary, err := lookPath(tesseract.Binary)
		if err != nil {
			return "", "", fmt.Errorf("needs tesseract installed, or %s for English: %w", rapid.Binary, err)
		}
		return "tesseract", binary, nil
	}
	pp := func() (string, string, error) {
		binary, err := lookPath(rapid.Binary)
		if err != nil {
			return "", "", fmt.Errorf("-engine rapid needs %s on PATH, see rapidocr/README.md: %w", rapid.Binary, err)
		}
		return "PP-OCRv6", binary, nil
	}
	switch engine {
	case "cells":
		return "", "", nil
	case "tesseract":
		return tess()
	case "rapid":
		return pp()
	case "auto":
		if lang == "eng" {
			if name, binary, err := pp(); err == nil {
				return name, binary, nil
			}
		}
		return tess()
	}
	return "", "", fmt.Errorf("unknown engine %q, want one of %s", engine, strings.Join(engines, ", "))
}

// readImage reads the image from path, or from stdin without one. Empty
// stdin is no image and no error.
func readImage(path string, stdin io.Reader) (image.Image, bool, error) {
	var data []byte
	var err error
	if path != "" {
		data, err = os.ReadFile(path)
	} else {
		data, err = io.ReadAll(stdin)
	}
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 && path == "" {
		return nil, false, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false, fmt.Errorf("reading the image: %w", err)
	}
	return img, true, nil
}

// cellReading is what the cell reader made of an image.
type cellReading struct {
	res cellread.Result
	err error
}

// textReading is what tesseract or PP-OCRv6 made of an image.
type textReading struct {
	by   string
	text string
	err  error
}

// choose takes the cell reader's text when it is confident the image is
// monospace text in a font it knows, such as code, and the other reader's
// otherwise. It says which it used, for -v.
func choose(cells cellReading, other textReading) (text, why string, err error) {
	if trusted(cells) {
		return cells.res.Text, fmt.Sprintf("cell reader (%s, match %.2f)", cells.res.Font, cells.res.Score), nil
	}
	why = fmt.Sprintf("%s (cell reader: %v)", other.by, cells.err)
	switch {
	case cells.err == nil && !mostlyLatin(cells.res):
		why = fmt.Sprintf("%s (cell reader: mostly wide characters)", other.by)
	case cells.err == nil:
		why = fmt.Sprintf("%s (cell reader match %.2f)", other.by, cells.res.Score)
	}
	return other.text, why, other.err
}

// trusted reports whether the cell reader's text should be used: it is
// confident of the cells it read, and they are most of the text.
func trusted(c cellReading) bool {
	return c.err == nil && c.res.Score >= monoConfident && mostlyLatin(c.res)
}

// mostlyLatin reports whether the cell reader read more characters itself
// than it left as wide. Text that is mostly Chinese or Japanese, such as a
// title, the other reader reads better whole.
func mostlyLatin(res cellread.Result) bool {
	wide, latin := 0, 0
	for _, w := range res.Wide {
		wide += w.Count
	}
	for _, r := range res.Text {
		if r != ' ' && r != '\n' && r != '\uFFFD' {
			latin++
		}
	}
	return latin >= wide
}

// fillWide puts text in place of the runs of wide characters the cell reader
// left in res, reading them with bands, all in one image, and then any
// still empty with eachRun, given each run enlarged and with a margin. It
// returns the text and how many characters are still unread. bands may be
// nil, without PP-OCRv6 or when another language is asked for.
func fillWide(img image.Image, res cellread.Result, bands func(image.Image, []image.Rectangle) ([]string, error), eachRun func(image.Image) (string, error)) (string, int) {
	texts := make([]string, len(res.Wide))
	if bands != nil {
		stack, rects := stackRuns(img, res.Wide)
		if got, err := bands(stack, rects); err == nil && len(got) == len(texts) {
			texts = got
		}
	}
	lines := strings.Split(res.Text, "\n")
	left := 0
	for i := len(res.Wide) - 1; i >= 0; i-- {
		w := res.Wide[i]
		t := strings.TrimSpace(texts[i])
		if t == "" {
			if k, err := eachRun(padded(upscale(crop(img, w.Bounds), upscaleBy), margin)); err == nil {
				t = strings.TrimSpace(k)
			}
		}
		if t == "" {
			left += w.Count
			continue
		}
		line := []rune(lines[w.Line])
		lines[w.Line] = string(line[:w.Start]) + t + string(line[w.Start+w.Count:])
	}
	return strings.Join(lines, "\n"), left
}

// stackRuns draws the runs, cut from img, one above another with space
// between, enlarged and with a margin, and returns the band of the result
// each run is in.
func stackRuns(img image.Image, runs []cellread.Wide) (*image.RGBA, []image.Rectangle) {
	const pad = 8
	w, h := 0, 0
	for _, r := range runs {
		w = max(w, r.Bounds.Dx())
		h = max(h, r.Bounds.Dy())
	}
	gap := h
	stack := image.NewRGBA(image.Rect(0, 0, w+2*pad, len(runs)*(h+gap)+2*pad))
	draw.Draw(stack, stack.Bounds(), image.NewUniform(edgeColour(img)), image.Point{}, draw.Src)
	var rects []image.Rectangle
	for i, r := range runs {
		y := pad + gap/2 + i*(h+gap)
		draw.Draw(stack, image.Rect(pad, y, pad+r.Bounds.Dx(), y+r.Bounds.Dy()), img, r.Bounds.Min.Add(img.Bounds().Min), draw.Src)
		band := image.Rect(0, y-gap/2, stack.Bounds().Dx(), y+h+gap/2)
		rects = append(rects, image.Rectangle{band.Min.Mul(upscaleBy), band.Max.Mul(upscaleBy)}.Add(image.Pt(margin, margin)))
	}
	big := upscale(stack, upscaleBy)
	return padded(big, margin), rects
}

// crop returns the part r of img, r being relative to img's corner.
func crop(img image.Image, r image.Rectangle) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min.Add(img.Bounds().Min), draw.Src)
	return out
}

// padded returns img with a margin of its most common edge colour around
// it, so text that runs to the edge of a tight selection is still found.
func padded(img image.Image, margin int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()+2*margin, b.Dy()+2*margin))
	draw.Draw(out, out.Bounds(), image.NewUniform(edgeColour(img)), image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(margin, margin, margin+b.Dx(), margin+b.Dy()), img, b.Min, draw.Src)
	return out
}

// edgeColour is the most common colour along the edge of img, which is
// nearly always the background.
func edgeColour(img image.Image) color.Color {
	b := img.Bounds()
	counts := map[color.RGBA]int{}
	add := func(x, y int) {
		r, g, bl, a := img.At(x, y).RGBA()
		counts[color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), uint8(a >> 8)}]++
	}
	for x := b.Min.X; x < b.Max.X; x++ {
		add(x, b.Min.Y)
		add(x, b.Max.Y-1)
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		add(b.Min.X, y)
		add(b.Max.X-1, y)
	}
	best, most := color.RGBA{255, 255, 255, 255}, 0
	for c, n := range counts {
		if n > most || n == most && rgbaLess(c, best) {
			best, most = c, n
		}
	}
	return best
}

// rgbaLess breaks ties between equally common colours, so the result
// doesn't depend on map order.
func rgbaLess(a, b color.RGBA) bool {
	if a.R != b.R {
		return a.R < b.R
	}
	if a.G != b.G {
		return a.G < b.G
	}
	if a.B != b.B {
		return a.B < b.B
	}
	return a.A < b.A
}

// readKorean reads img with tesseract's Korean model, for when PP-OCRv6,
// whose model reads Chinese, Japanese and English but not Hangul, found
// nothing. The text counts only if it has Hangul in it; tesseract makes
// something of any picture.
func readKorean(img image.Image) (string, error) {
	binary, err := exec.LookPath(tesseract.Binary)
	if err != nil {
		return "", rapid.ErrNoText
	}
	text, err := tesseract.Recognize(binary, "kor", img)
	if err != nil || !hasHangul(text) {
		return "", rapid.ErrNoText
	}
	return text, nil
}

// hasHangul reports whether s has a Korean letter in it.
func hasHangul(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

// upscale returns img enlarged by a whole factor. A smooth filter matters:
// nearest neighbour read digits worse than no upscale at all, 0 turning to 8.
func upscale(img image.Image, by int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*by, b.Dy()*by))
	draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Src, nil)
	return out
}
