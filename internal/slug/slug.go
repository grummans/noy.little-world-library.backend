// Package slug turns Vietnamese titles into URL slugs.
package slug

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// MaxLen is the longest slug the API accepts.
const MaxLen = 80

// Make lowercases, strips diacritics and joins words with dashes:
// "Động vật quanh bé" → "dong-vat-quanh-be".
func Make(title string) string {
	lowered := strings.ReplaceAll(strings.ToLower(title), "đ", "d")

	var b strings.Builder
	lastDash := true
	for _, r := range norm.NFD.String(lowered) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if b.Len() < MaxLen {
				b.WriteRune(r)
			}
			lastDash = false
		default:
			if !lastDash && b.Len() < MaxLen {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// Valid reports whether slug is already in the canonical shape.
func Valid(value string) bool {
	if value == "" || len(value) > MaxLen {
		return false
	}

	lastDash := true
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			lastDash = false
		case r == '-':
			if lastDash {
				return false
			}
			lastDash = true
		default:
			return false
		}
	}
	return !lastDash
}
