// Package rapid reads text with PP-OCRv6 small through the keming-rapidocr
// helper in rapidocr/. It reads web pages, chat and documents better than
// tesseract, at about 2s a read instead of 0.4s, and runs offline once the
// helper has its models.
package rapid

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os/exec"
	"slices"
	"strings"
)

// Binary is the helper looked up on PATH.
const Binary = "keming-rapidocr"

// ErrNoText means the reader found nothing to read.
var ErrNoText = errors.New("no text found")

// Reader is a running helper, waiting for an image.
type Reader struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	out, stderr bytes.Buffer
}

// Start runs the helper ahead of the image. Loading its models takes over a
// second, which can pass while the caller is still getting the image.
func Start(binary string) (*Reader, error) {
	r := &Reader{cmd: exec.Command(binary)}
	r.cmd.Stdout = &r.out
	r.cmd.Stderr = &r.stderr
	stdin, err := r.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	r.stdin = stdin
	if err := r.cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", Binary, err)
	}
	return r, nil
}

// Read sends img to the helper and puts the boxes it finds into lines. The
// image goes over stdin so a screenshot never lands in a temp file. A Reader
// reads once.
func (r *Reader) Read(img image.Image) (string, error) {
	return r.read(img, func(box) bool { return true })
}

// ReadWithin reads img, as Read does, keeping only the text found within
// part of it, for a picture sent with a margin around it. A box may reach
// half the margin past the part, as boxes are drawn a little wider than
// their text; one reaching further is something the margin made, such as
// the edge between a gradient and a flat margin, read as 1.
func (r *Reader) ReadWithin(img image.Image, within image.Rectangle) (string, error) {
	slack := min(within.Min.X-img.Bounds().Min.X, within.Min.Y-img.Bounds().Min.Y) / 2
	outer := within.Inset(-slack)
	return r.read(img, func(b box) bool {
		for _, p := range b.Box {
			if !image.Pt(int(p[0]), int(p[1])).In(outer) {
				return false
			}
		}
		return true
	})
}

// read sends img to the helper and puts the boxes it finds, those keep
// accepts, into lines.
func (r *Reader) read(img image.Image, keep func(box) bool) (string, error) {
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	err := encoder.Encode(r.stdin, img)
	if cerr := r.stdin.Close(); err == nil {
		err = cerr
	}
	if werr := r.cmd.Wait(); werr != nil {
		return "", fmt.Errorf("%s: %w: %s", Binary, werr, lastLine(r.stderr.String()))
	}
	if err != nil {
		return "", err
	}
	var boxes []box
	if err := json.Unmarshal(r.out.Bytes(), &boxes); err != nil {
		return "", fmt.Errorf("%s: %w", Binary, err)
	}
	boxes = slices.DeleteFunc(boxes, func(b box) bool { return !keep(b) })
	text := assembleLines(boxes)
	if text == "" {
		return "", ErrNoText
	}
	return text, nil
}

// Close stops a helper that won't be given an image.
func (r *Reader) Close() {
	r.stdin.Close()
	if r.cmd.ProcessState == nil {
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}
}

// lastLine keeps the end of a Python traceback, which names the error.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// box is one run of text the helper found, usually a word or a line.
type box struct {
	Text string       `json:"text"`
	Box  [][2]float64 `json:"box"`
}
type textLine struct {
	top, bottom, centre float64
	words               []box
}

// assembleLines puts boxes into lines of text. The reader returns a box per
// word wherever words are spaced widely, as in justified text, and in no
// particular order, so boxes whose middles sit level form a line, read left to
// right. A gap taller than a line between lines becomes a blank line, as it
// is usually a new paragraph or message.
func assembleLines(boxes []box) string {
	var lines []*textLine
	for _, b := range byCentre(boxes) {
		top, bottom, _ := extent(b)
		centre := (top + bottom) / 2
		var line *textLine
		if n := len(lines); n > 0 {
			l := lines[n-1]
			if math.Abs(centre-l.centre) < min(bottom-top, l.bottom-l.top)/2 {
				line = l
			}
		}
		if line == nil {
			line = &textLine{top: top, bottom: bottom, centre: centre}
			lines = append(lines, line)
		}
		line.words = append(line.words, b)
		line.top, line.bottom = min(line.top, top), max(line.bottom, bottom)
	}

	heights := make([]float64, len(lines))
	for i, l := range lines {
		heights[i] = l.bottom - l.top
	}
	slices.Sort(heights)

	var out []string
	for i, l := range lines {
		if i > 0 && l.top-lines[i-1].bottom > heights[len(heights)/2] {
			out = append(out, "")
		}
		slices.SortStableFunc(l.words, func(a, b box) int {
			_, _, la := extent(a)
			_, _, lb := extent(b)
			return cmpFloat(la, lb)
		})
		words := make([]string, len(l.words))
		for j, w := range l.words {
			words[j] = strings.TrimSpace(w.Text)
		}
		out = append(out, strings.Join(words, " "))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func byCentre(boxes []box) []box {
	sorted := slices.Clone(boxes)
	slices.SortStableFunc(sorted, func(a, b box) int {
		ta, ba, _ := extent(a)
		tb, bb, _ := extent(b)
		return cmpFloat(ta+ba, tb+bb)
	})
	return sorted
}

// extent is the top, bottom and left edge of a box.
func extent(b box) (top, bottom, left float64) {
	top, bottom, left = math.Inf(1), math.Inf(-1), math.Inf(1)
	for _, p := range b.Box {
		left, top, bottom = min(left, p[0]), min(top, p[1]), max(bottom, p[1])
	}
	return top, bottom, left
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
