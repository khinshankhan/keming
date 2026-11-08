//go:build unix

package rapid

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fake writes a shell script standing in for the helper.
func fake(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), Binary)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// word is a box from x0 to x1 and y0 to y1, as the helper reports one.
func word(text string, x0, y0, x1, y1 float64) box {
	return box{Text: text, Box: [][2]float64{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}}
}

func TestAssembleLines(t *testing.T) {
	tests := []struct {
		name  string
		boxes []box
		want  string
	}{
		{"words in any order read left to right", []box{
			word("port", 300, 10, 340, 30), word("Ask", 10, 10, 40, 30), word("any", 60, 11, 90, 31),
		}, "Ask any port"},
		{"lines read top to bottom", []box{
			word("second", 10, 40, 80, 60), word("line", 90, 40, 130, 60),
			word("first", 10, 10, 60, 30), word("line", 70, 10, 110, 30),
		}, "first line\nsecond line"},
		{"a short box sits on its line", []box{
			word("if", 10, 10, 30, 32), word("{", 40, 14, 48, 34), word("x", 10, 40, 20, 60),
		}, "if {\nx"},
		{"a gap taller than a line is a new paragraph", []box{
			word("Tomas", 10, 10, 70, 30), word("hi", 10, 36, 30, 56), word("Ana", 10, 90, 50, 110),
		}, "Tomas\nhi\n\nAna"},
		{"nothing found", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assembleLines(tt.boxes); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRead(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	tests := []struct {
		name    string
		script  string
		want    string
		wantErr string
	}{
		{"puts boxes into lines", `cat >/dev/null; printf '[{"text": "b", "score": 0.9, "box": [[50,0],[60,0],[60,10],[50,10]]}, {"text": "a", "score": 0.9, "box": [[0,0],[10,0],[10,10],[0,10]]}]'`, "a b", ""},
		{"reports the end of a traceback", `cat >/dev/null; printf 'Traceback\n  File x\nModuleNotFoundError: No module named rapidocr\n' >&2; exit 1`, "", "No module named rapidocr"},
		{"rejects output that isn't boxes", `cat >/dev/null; printf 'oops'`, "", Binary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Start(fake(t, tt.script))
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Read(img)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got error %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	r, err := Start(fake(t, `cat >/dev/null; printf '[]'`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(img); !errors.Is(err, ErrNoText) {
		t.Fatalf("no boxes: got %v, want ErrNoText", err)
	}
}

func TestCloseStopsAnUnusedHelper(t *testing.T) {
	// Stands in for a helper still loading its models.
	r, err := Start(fake(t, `exec sleep 30`))
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if ws, ok := r.cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() {
		t.Fatalf("helper ended %v, want it killed", r.cmd.ProcessState)
	}
}
