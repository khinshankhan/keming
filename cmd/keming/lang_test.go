package main

import (
	"strings"
	"testing"
)

func TestTesseractLang(t *testing.T) {
	tests := []struct {
		code, want, wantErr string
	}{
		{"en", "eng", ""},
		{"EN", "eng", ""},
		{"en-US", "eng", ""},
		{"en_GB.UTF-8", "eng", ""},
		{"da", "dan", ""},
		{"fr-CA", "fra", ""},
		{"pt_BR", "por", ""},
		{"vi", "vie", ""},
		{"ko", "kor", ""},
		{"ja", "jpn", ""},
		{"zh", "chi_sim", ""},
		{"zh-CN", "chi_sim", ""},
		{"zh-Hans", "chi_sim", ""},
		{"zh-TW", "chi_tra", ""},
		{"zh_HK", "chi_tra", ""},
		{"zh-Hant-TW", "chi_tra", ""},
		{"sr", "srp", ""},
		{"sr-Latn", "srp_latn", ""},
		{"nb", "nor", ""},
		{"eng", "", "unknown language"},
		{"chi_sim", "", "unknown language"},
		{"xx", "", "unknown language"},
		{"", "", "unknown language"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got, err := tesseractLang(tt.code)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("tesseractLang(%q) = %q, %v; want error %q", tt.code, got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("tesseractLang(%q) = %q, %v; want %q", tt.code, got, err, tt.want)
			}
		})
	}
}
