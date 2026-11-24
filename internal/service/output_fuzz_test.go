package service

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/clipperhouse/uax29/v2/graphemes"
	"golang.org/x/text/unicode/norm"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func FuzzOutputPolicy(f *testing.F) {
	for _, pair := range [][2]string{{"visual", "video"}, {"auditory", "podcast"}, {"read_write", "article"}, {"kinesthetic", "exercise"}, {"unknown", "video"}} {
		f.Add("https://example.com/a?q=é'", "Title", "Explanation", pair[1], pair[0], uint8(5))
	}
	for _, raw := range []string{"javascript:alert(1)", "/relative", "https://user@example.com", "https://example.com/\u202e", "http://[::1]/", "https://example.com/" + strings.Repeat("é", 2048)} {
		f.Add(raw, "\x00\u202e"+strings.Repeat("界", 201), "\t\u200b"+strings.Repeat("é", 1001), " VIDEO ", "visual", uint8(4))
	}
	f.Add("https://bücher.example", "Unicode host", "می\u200cخواهم क्\u200dष 🧑🏽\u200d💻", "video", "visual", uint8(1))
	f.Add("https://example.com", "\xff\u2066\u200d\u2069", "\u200c\u202e\u200d\t", "video", "visual", uint8(1))
	f.Add("https://example.com", "e"+strings.Repeat("\u0301", 200), strings.Repeat("x", 998)+"👩\u200d💻", "video", "visual", uint8(1))
	f.Add("https://example.com", strings.Repeat("x", 198)+"\u0d4e b", strings.Repeat("x", 998)+"ب\u200cت", "video", "visual", uint8(1))
	f.Fuzz(func(t *testing.T, rawURL, title, explanation, kind, profile string, count uint8) {
		resources := make([]domain.Resource, int(count%8))
		for i := range resources {
			resources[i] = domain.Resource{URL: rawURL, Title: title, ResourceType: kind}
		}
		input := []domain.PlanItem{{Explanation: explanation, Resources: resources}}
		items, dropped := sanitizeOutput(input, profile, false)
		if len(items) != 1 || len(items[0].Resources) > 3 || dropped+len(items[0].Resources) != len(resources) {
			t.Fatalf("invalid output size or drop count: items=%v dropped=%d", items, dropped)
		}
		display, displayDropped := sanitizeOutput(input, profile, true)
		if len(display) != 1 || len(display[0].Resources) != len(items[0].Resources) || displayDropped != dropped {
			t.Fatalf("display policy changed resource selection: items=%v dropped=%d", display, displayDropped)
		}
		checkOutputText(t, items[0].Explanation, maxExplanationRunes, false)
		checkDisplayPrefix(t, explanation, display[0].Explanation, maxExplanationRunes)
		allowed := map[string][]string{
			"visual":      {"video", "diagram", "infographic"},
			"auditory":    {"podcast", "lecture", "audio-course"},
			"read_write":  {"article", "textbook", "documentation"},
			"kinesthetic": {"exercise", "lab", "coding-challenge"},
		}
		for i, resource := range items[0].Resources {
			u, err := url.Parse(resource.URL)
			if err != nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || utf8.RuneCountInString(resource.URL) > 2048 {
				t.Fatalf("invalid resource URL %q: %v", resource.URL, err)
			}
			checkOutputText(t, resource.Title, maxResourceTitleRunes, false)
			displayResource := display[0].Resources[i]
			checkDisplayPrefix(t, title, displayResource.Title, maxResourceTitleRunes)
			if displayResource.URL != resource.URL || displayResource.ResourceType != resource.ResourceType {
				t.Fatalf("display policy changed URL or type: legacy=%+v display=%+v", resource, displayResource)
			}
			if !slices.Contains(allowed[profile], resource.ResourceType) {
				t.Fatalf("type %q is not allowed for profile %q", resource.ResourceType, profile)
			}
		}
	})
}

func checkOutputText(t *testing.T, text string, limit int, joins bool) {
	t.Helper()
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > limit || strings.TrimSpace(text) != text || strings.Contains(text, "  ") {
		t.Fatalf("invalid output text (limit %d): %q", limit, text)
	}
	for offset, r := range text {
		if (unicode.IsSpace(r) && r != ' ') ||
			(unicode.In(r, unicode.Cc, unicode.Cf) && (!joins || !referenceJoiner(text, offset, r))) {
			t.Fatalf("unexpected whitespace or control %U in %q", r, text)
		}
	}
}

func checkDisplayPrefix(t *testing.T, input, got string, limit int) {
	t.Helper()
	checkOutputText(t, got, limit, true)
	var filtered strings.Builder
	for offset, r := range input {
		if unicode.IsSpace(r) || !unicode.In(r, unicode.Cc, unicode.Cf) || referenceJoiner(input, offset, r) {
			filtered.WriteRune(r)
		}
	}
	full := strings.Join(strings.Fields(filtered.String()), " ")
	end := 0
	remaining := limit
	clusters := graphemes.FromString(full)
	for clusters.Next() {
		cluster := clusters.Value()
		remaining -= utf8.RuneCountInString(cluster)
		if remaining < 0 {
			break
		}
		last, _ := utf8.DecodeLastRuneInString(cluster)
		if !unicode.IsSpace(last) && last != '\u200c' {
			end = clusters.End()
		}
	}
	if want := full[:end]; got != want {
		t.Fatalf("display text (%d runes) = %q, want the complete cluster prefix %q", limit, got, want)
	}
}

// Check the documented contexts using Unicode data and independent lookups.
func referenceJoiner(text string, offset int, joiner rune) bool {
	if (joiner != '\u200c' && joiner != '\u200d') || offset == 0 {
		return false
	}
	left := text[:offset]
	right, _ := utf8.DecodeRuneInString(text[offset+len("\u200c"):])
	has := func(r rune, intervals []displayRuneRange) bool {
		return slices.ContainsFunc(intervals, func(interval displayRuneRange) bool {
			return interval.lo <= r && r <= interval.hi
		})
	}
	mark := func(r rune) bool { return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) }
	switch joiner {
	case '\u200c':
		if has(right, arabicJoinsPreceding) {
			base, _ := utf8.DecodeLastRuneInString(strings.TrimRightFunc(left, mark))
			if has(base, arabicJoinsFollowing) {
				return true
			}
		}
	case '\u200d':
		if has(right, extendedPictographic) {
			base, _ := utf8.DecodeLastRuneInString(strings.TrimRightFunc(left, func(r rune) bool {
				return mark(r) || has(r, graphemeExtendAdditional)
			}))
			if has(base, extendedPictographic) {
				return true
			}
		}
	}
	virama, width := utf8.DecodeLastRuneInString(left)
	if norm.NFD.PropertiesString(string(virama)).CCC() != 9 {
		return false
	}
	base, _ := utf8.DecodeLastRuneInString(strings.TrimRightFunc(left[:len(left)-width], unicode.IsMark))
	if !unicode.IsLetter(base) {
		return false
	}
	for _, script := range unicode.Scripts {
		if unicode.Is(script, base) && unicode.Is(script, virama) {
			return joiner == '\u200d' || (unicode.IsLetter(right) && unicode.Is(script, right))
		}
	}
	return false
}
