// Package tesseract reads text with the tesseract command, which handles
// many languages and costs little, but reads less well than PP-OCRv6.
package tesseract

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"strings"
)

// Binary is the command looked up on PATH.
const Binary = "tesseract"

// ErrNoText means tesseract found nothing to read.
var ErrNoText = errors.New("no text found")

// Recognize reads the text in img. The image goes over stdin so a screenshot,
// which may well hold something private, never lands in a temp file. Screen
// text reads far better enlarged first.
func Recognize(binary, lang string, img image.Image) (string, error) {
	var in bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&in, img); err != nil {
		return "", err
	}

	// psm 4 reads one column of text in mixed sizes. psm 3 read ls -l output
	// column by column, and psm 6 lost words from forms and video calls.
	var out, stderr bytes.Buffer
	cmd := exec.Command(binary, "stdin", "stdout", "-l", lang, "--oem", "1", "--psm", "4")
	cmd.Stdin = &in
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	// Trimming also drops the form feed tesseract ends each page with.
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", ErrNoText
	}
	return text, nil
}
