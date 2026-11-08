package cellread

import (
	"encoding/binary"
	"slices"
	"testing"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonoitalic"
)

// collection joins font files into one collection file, as Menlo.ttc on
// macOS holds its regular, bold and italics.
func collection(t *testing.T, fonts ...[]byte) []byte {
	t.Helper()
	be := binary.BigEndian
	header := 12 + 4*len(fonts)
	dirs := header
	for _, f := range fonts {
		dirs += 12 + 16*int(be.Uint16(f[4:]))
	}
	out := make([]byte, dirs)
	copy(out, "ttcf")
	be.PutUint16(out[4:], 1)
	be.PutUint32(out[8:], uint32(len(fonts)))
	at := header
	for i, f := range fonts {
		be.PutUint32(out[12+4*i:], uint32(at))
		n := int(be.Uint16(f[4:]))
		copy(out[at:], f[:12])
		for r := range n {
			rec := f[12+16*r : 28+16*r]
			off, length := be.Uint32(rec[8:]), be.Uint32(rec[12:])
			dst := out[at+12+16*r:]
			copy(dst, rec)
			be.PutUint32(dst[8:], uint32(len(out)))
			out = append(out, f[off:off+length]...)
			for len(out)%4 != 0 {
				out = append(out, 0)
			}
		}
		at += 12 + 16*n
	}
	return out
}

func TestLoadFontsReadsACollection(t *testing.T) {
	fonts, err := LoadFonts("Mono.ttc", collection(t, gomono.TTF, gomonobold.TTF, gomonoitalic.TTF))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range fonts {
		names = append(names, f.Name)
	}
	// The italic is left out, as with separate files.
	if want := []string{"Mono.ttc#0", "Mono.ttc#1"}; !slices.Equal(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}

	want := "drag over it, print it\nl1 O0 = {quit}"
	got, err := Read(drawText(t, gomonobold.TTF, want, dark, 16), fonts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != want || got.Font != "Mono.ttc#1" {
		t.Fatalf("read %q in %s, want %q in the bold, Mono.ttc#1", got.Text, got.Font, want)
	}
}
