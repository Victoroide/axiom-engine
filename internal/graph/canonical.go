package graph

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Canonical folds case, removes format characters and collapses whitespace in NFC.
func Canonical(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	folded := cases.Fold().String(strings.Join(strings.Fields(s), " "))
	// x/text cases.Fold maps uppercase Cherokee to lowercase, while Unicode
	// CaseFolding maps lowercase Cherokee to uppercase; follow Unicode.
	folded = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cherokee, r) {
			return unicode.ToUpper(r)
		}
		return r
	}, folded)
	return norm.NFC.String(folded)
}
