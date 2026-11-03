package service

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func FuzzOutputPolicy(f *testing.F) {
	for _, pair := range [][2]string{{"visual", "video"}, {"auditory", "podcast"}, {"read_write", "article"}, {"kinesthetic", "exercise"}, {"unknown", "video"}} {
		f.Add("https://example.com/a?q=é'", "Title", "Explanation", pair[1], pair[0], uint8(5))
	}
	for _, raw := range []string{"javascript:alert(1)", "/relative", "https://user@example.com", "https://example.com/\u202e", "http://[::1]/", "https://example.com/" + strings.Repeat("é", 2048)} {
		f.Add(raw, "\x00\u202e"+strings.Repeat("界", 201), "\t\u200b"+strings.Repeat("é", 1001), " VIDEO ", "visual", uint8(4))
	}
	f.Fuzz(func(t *testing.T, rawURL, title, explanation, kind, profile string, count uint8) {
		resources := make([]domain.Resource, int(count%8))
		for i := range resources {
			resources[i] = domain.Resource{URL: rawURL, Title: title, ResourceType: kind}
		}
		items, dropped := sanitizeOutput([]domain.PlanItem{{Explanation: explanation, Resources: resources}}, profile)
		if len(items) != 1 || len(items[0].Resources) > 3 || dropped+len(items[0].Resources) != len(resources) {
			t.Fatalf("invalid output size or drop count: items=%v dropped=%d", items, dropped)
		}
		checkOutputText(t, items[0].Explanation, 1000)
		allowed := map[string][]string{
			"visual":      {"video", "diagram", "infographic"},
			"auditory":    {"podcast", "lecture", "audio-course"},
			"read_write":  {"article", "textbook", "documentation"},
			"kinesthetic": {"exercise", "lab", "coding-challenge"},
		}
		for _, resource := range items[0].Resources {
			u, err := url.Parse(resource.URL)
			if err != nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || utf8.RuneCountInString(resource.URL) > 2048 {
				t.Fatalf("invalid resource URL %q: %v", resource.URL, err)
			}
			checkOutputText(t, resource.Title, 200)
			if !slices.Contains(allowed[profile], resource.ResourceType) {
				t.Fatalf("type %q is not allowed for profile %q", resource.ResourceType, profile)
			}
		}
	})
}

func checkOutputText(t *testing.T, text string, limit int) {
	t.Helper()
	if utf8.RuneCountInString(text) > limit || strings.ContainsFunc(text, func(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf) }) {
		t.Fatalf("invalid output text (limit %d): %q", limit, text)
	}
}
