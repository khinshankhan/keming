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
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/khinshankhan/keming/cellread"
	"github.com/khinshankhan/keming/rapid"
	"github.com/khinshankhan/keming/tesseract"
	"golang.org/x/image/draw"
)

const (
	// Screen text is small for both general readers. 2x read browser and
	// terminal text far better than the original size, and 3x was no better.
	upscaleBy = 2

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
	switch name {
	case "PP-OCRv6":
		r, err := rapid.Start(binary)
		if err != nil {
			return err
		}
		defer r.Close()
		read = func(img image.Image) (string, error) { return r.Read(upscale(img, upscaleBy)) }
	case "tesseract":
		read = func(img image.Image) (string, error) {
			return tesseract.Recognize(binary, *lang, upscale(img, upscaleBy))
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
	switch {
	case read == nil:
		c := readCells(img, fonts)
		text, err = c.res.Text, c.err
		why = fmt.Sprintf("cell reader (%s, match %.2f), as asked", c.res.Font, c.res.Score)
	case !useCells:
		text, err = read(img)
		why = name + ", as asked"
	default:
		c := readCells(img, fonts)
		if c.err == nil && c.res.Score >= monoConfident {
			text, why, err = choose(c, textReading{})
			break
		}
		t, terr := read(img)
		text, why, err = choose(c, textReading{name, t, terr})
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
	if cells.err == nil && cells.res.Score >= monoConfident {
		return cells.res.Text, fmt.Sprintf("cell reader (%s, match %.2f)", cells.res.Font, cells.res.Score), nil
	}
	why = fmt.Sprintf("%s (cell reader: %v)", other.by, cells.err)
	if cells.err == nil {
		why = fmt.Sprintf("%s (cell reader match %.2f)", other.by, cells.res.Score)
	}
	return other.text, why, other.err
}

// upscale returns img enlarged by a whole factor. A smooth filter matters:
// nearest neighbour read digits worse than no upscale at all, 0 turning to 8.
func upscale(img image.Image, by int) *image.RGBA {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*by, b.Dy()*by))
	draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Src, nil)
	return out
}
