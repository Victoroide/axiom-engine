package service

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/clipperhouse/uax29/v2/graphemes"
	"golang.org/x/text/unicode/norm"
)

const (
	zwnj        = '\u200c'
	zwj         = '\u200d'
	viramaClass = 9
)

func cleanDisplayText(text string, limit int) string {
	var out strings.Builder
	count := 0
	space := false
	for offset, r := range text {
		if unicode.IsSpace(r) {
			space = count > 0
			continue
		}
		if outputControl(r) && !displayJoinerAllowed(text, offset, r) {
			continue
		}
		if space {
			out.WriteByte(' ')
			count++
			space = false
		}
		if count <= limit {
			out.WriteRune(r)
			count++
		}
		// One extra code point establishes the boundary at the budget.
		if count > limit {
			break
		}
	}
	if count <= limit {
		return out.String()
	}
	return graphemePrefix(out.String(), limit, func(cluster string) bool {
		// Both ZWNJ contexts require a following letter. A cluster
		// ending in ZWNJ or space can only appear inside the prefix.
		return !strings.HasSuffix(cluster, " ") && !strings.HasSuffix(cluster, string(zwnj))
	})
}

func graphemePrefix(text string, limit int, keepEnd func(cluster string) bool) string {
	end := 0
	clusters := graphemes.FromString(text)
	for clusters.Next() {
		cluster := clusters.Value()
		limit -= utf8.RuneCountInString(cluster)
		if limit < 0 {
			break
		}
		if keepEnd(cluster) {
			end = clusters.End()
		}
	}
	return text[:end]
}

func displayJoinerAllowed(text string, offset int, joiner rune) bool {
	if (joiner != zwnj && joiner != zwj) || offset == 0 {
		return false
	}
	before := text[:offset]
	next, _ := utf8.DecodeRuneInString(text[offset+utf8.RuneLen(joiner):])
	if joiner == zwj && inDisplayRanges(next, extendedPictographic) {
		base := lastBeforeMarks(before, emojiGraphemeExtend)
		if inDisplayRanges(base, extendedPictographic) {
			return true
		}
	}
	if joiner == zwnj && inDisplayRanges(next, arabicJoinsPreceding) {
		base := lastBeforeMarks(before, nonspacingOrEnclosingMark)
		if inDisplayRanges(base, arabicJoinsFollowing) {
			return true
		}
	}
	virama, size := utf8.DecodeLastRuneInString(before)
	if norm.NFC.PropertiesString(before[len(before)-size:]).CCC() != viramaClass {
		return false
	}
	letter := lastBeforeMarks(before[:len(before)-size], unicode.IsMark)
	if !unicode.IsLetter(letter) || joiner == zwnj && !unicode.IsLetter(next) {
		return false
	}
	for _, script := range unicode.Scripts {
		if unicode.Is(script, virama) && unicode.Is(script, letter) {
			return joiner == zwj || unicode.Is(script, next)
		}
	}
	return false
}

func nonspacingOrEnclosingMark(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me)
}

func emojiGraphemeExtend(r rune) bool {
	return nonspacingOrEnclosingMark(r) || inDisplayRanges(r, graphemeExtendAdditional)
}

func lastBeforeMarks(text string, isMark func(rune) bool) rune {
	for len(text) > 0 {
		r, size := utf8.DecodeLastRuneInString(text)
		if !isMark(r) {
			return r
		}
		text = text[:len(text)-size]
	}
	return utf8.RuneError
}

type displayRuneRange struct{ lo, hi rune }

func inDisplayRanges(r rune, ranges []displayRuneRange) bool {
	_, found := slices.BinarySearchFunc(ranges, r, func(interval displayRuneRange, target rune) int {
		switch {
		case interval.hi < target:
			return -1
		case interval.lo > target:
			return 1
		default:
			return 0
		}
	})
	return found
}
