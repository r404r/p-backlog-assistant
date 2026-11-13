package fileutil

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func CleanComponent(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`\:*?"<>|/`, r) {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimRight(s, ". ")
	if s == "" || s == "." || s == ".." {
		s = "_"
	}
	stem := strings.ToUpper(strings.SplitN(s, ".", 2)[0])
	reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9'
	if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
		reserved = reserved || strings.Contains("|COM¹|COM²|COM³|LPT¹|LPT²|LPT³|", "|"+stem+"|")
	}
	if reserved {
		if i := strings.IndexByte(s, '.'); i >= 0 {
			s = s[:i] + "_" + s[i:]
		} else {
			s += "_"
		}
	}
	return s
}
func FitComponent(base, suffix, ext string) string {
	max := 255 - len(suffix) - len(ext)
	if max < 1 {
		max = 1
	}
	for len(base) > max {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	base = strings.TrimRight(base, ". ")
	if base == "" {
		base = "_"
	}
	return base + suffix + ext
}

func SafeAttachmentFilename(name string) string {
	name = CleanComponent(strings.ToValidUTF8(name, "_"))
	ext := filepath.Ext(name)
	if len(ext) > 32 {
		ext = ""
	}
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		base = "_"
	}
	result := FitComponent(base, "", ext)
	// 短縮後も予約名と末尾を確認する。
	result = CleanComponent(result)
	if len(result) > 255 {
		result = FitComponent(strings.TrimSuffix(result, ext), "", ext)
	}
	return result
}
