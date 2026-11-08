//go:build unix

package rapid

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"slices"
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

func TestReadWithinDropsBoxesOutside(t *testing.T) {
	// A 100x60 picture inside a 32 pixel margin, as keming's retry sends it.
	img := image.NewRGBA(image.Rect(0, 0, 164, 124))
	within := image.Rect(32, 32, 132, 92)
	tests := []struct {
		name, boxes, want string
	}{
		{"text inside is kept", `{"text": "名探偵", "score": 1, "box": [[45,38],[120,38],[120,90],[45,90]]}`, "名探偵"},
		// A gradient against a flat margin read as one box over everything.
		{"a box over the margin is dropped", `{"text": "1", "score": 0.48, "box": [[0,0],[163,0],[163,123],[0,123]]}`, ""},
		{"a box a little over the edge is kept", `{"text": "ok", "score": 0.9, "box": [[20,30],[60,30],[60,50],[20,50]]}`, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Start(fake(t, "cat >/dev/null; printf '%s' '["+tt.boxes+"]'"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.ReadWithin(img, within)
			if tt.want == "" {
				if !errors.Is(err, ErrNoText) {
					t.Fatalf("got %q, %v, want ErrNoText", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestReadBands(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 120))
	bands := []image.Rectangle{image.Rect(0, 0, 200, 40), image.Rect(0, 40, 200, 80), image.Rect(0, 80, 200, 120)}
	// Two boxes in the first band, out of order, none in the second.
	boxes := `[{"text": "界", "box": [[60,5],[90,5],[90,35],[60,35]]},` +
		`{"text": "世", "box": [[10,5],[40,5],[40,35],[10,35]]},` +
		`{"text": "注释", "box": [[10,85],[70,85],[70,115],[10,115]]}]`
	r, err := Start(fake(t, "cat >/dev/null; printf '%s' '"+boxes+"'"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadBands(img, bands)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"世界", "", "注释"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
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
