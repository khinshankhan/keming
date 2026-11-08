package main

import (
	"fmt"
	"slices"
	"strings"
)

// isoToTesseract maps ISO 639-1 language codes to tesseract's model names,
// for every language tesseract has a model for and ISO 639-1 a code.
var isoToTesseract = map[string]string{
	"af": "afr", "am": "amh", "ar": "ara", "as": "asm", "az": "aze",
	"be": "bel", "bg": "bul", "bn": "ben", "bo": "bod", "br": "bre",
	"bs": "bos", "ca": "cat", "co": "cos", "cs": "ces", "cy": "cym",
	"da": "dan", "de": "deu", "dv": "div", "dz": "dzo", "el": "ell",
	"en": "eng", "eo": "epo", "es": "spa", "et": "est", "eu": "eus",
	"fa": "fas", "fi": "fin", "fo": "fao", "fr": "fra", "fy": "fry",
	"ga": "gle", "gd": "gla", "gl": "glg", "gu": "guj", "he": "heb",
	"hi": "hin", "hr": "hrv", "ht": "hat", "hu": "hun", "hy": "hye",
	"id": "ind", "is": "isl", "it": "ita", "iu": "iku", "ja": "jpn",
	"jv": "jav", "ka": "kat", "kk": "kaz", "km": "khm", "kn": "kan",
	"ko": "kor", "ku": "kmr", "ky": "kir", "la": "lat", "lb": "ltz",
	"lo": "lao", "lt": "lit", "lv": "lav", "mi": "mri", "mk": "mkd",
	"ml": "mal", "mn": "mon", "mr": "mar", "ms": "msa", "mt": "mlt",
	"my": "mya", "nb": "nor", "ne": "nep", "nl": "nld", "nn": "nor",
	"no": "nor", "oc": "oci", "or": "ori", "pa": "pan", "pl": "pol",
	"ps": "pus", "pt": "por", "qu": "que", "ro": "ron", "ru": "rus",
	"sa": "san", "sd": "snd", "si": "sin", "sk": "slk", "sl": "slv",
	"sq": "sqi", "sr": "srp", "su": "sun", "sv": "swe", "sw": "swa",
	"ta": "tam", "te": "tel", "tg": "tgk", "th": "tha", "ti": "tir",
	"tl": "tgl", "to": "ton", "tr": "tur", "tt": "tat", "ug": "uig",
	"uk": "ukr", "ur": "urd", "uz": "uzb", "vi": "vie", "yi": "yid",
	"yo": "yor", "zh": "chi_sim",
}

// tesseractLang turns a language code as people write it, an ISO 639-1 code
// with an optional script or region (en, pt-BR, zh-Hant, or a locale such
// as zh_TW.UTF-8), into tesseract's model name. Chinese is traditional for
// Taiwan, Hong Kong and Macau, or with the Hant script.
func tesseractLang(code string) (string, error) {
	code, _, _ = strings.Cut(strings.ToLower(code), ".")
	code, _, _ = strings.Cut(code, "@")
	tags := strings.FieldsFunc(code, func(r rune) bool { return r == '-' || r == '_' })
	if len(tags) == 0 {
		return "", fmt.Errorf("unknown language %q, want an ISO 639-1 code such as en, fr or zh-TW", code)
	}
	tess, ok := isoToTesseract[tags[0]]
	if !ok {
		return "", fmt.Errorf("unknown language %q, want an ISO 639-1 code such as en, fr or zh-TW", code)
	}
	rest := tags[1:]
	switch tags[0] {
	case "zh":
		if slices.Contains(rest, "hant") || slices.Contains(rest, "tw") || slices.Contains(rest, "hk") || slices.Contains(rest, "mo") {
			return "chi_tra", nil
		}
	case "sr":
		if slices.Contains(rest, "latn") {
			return "srp_latn", nil
		}
	case "az", "uz":
		if slices.Contains(rest, "cyrl") {
			return tess + "_cyrl", nil
		}
	}
	return tess, nil
}

// readByPPOCR reports whether PP-OCRv6 reads the language, as tesseract
// names it: one model covers English, Chinese and Japanese, and read titles,
// prose and terminal lines in all three exactly. It returns nothing or
// confident junk for Korean.
func readByPPOCR(tess string) bool {
	return tess == "eng" || tess == "chi_sim" || tess == "chi_tra" || tess == "jpn"
}
