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
	return norm.NFC.String(cases.Fold().String(strings.Join(strings.Fields(s), " ")))
}
