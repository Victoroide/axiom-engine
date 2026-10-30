package service

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func TestOutputPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, url string
		keep      bool
	}{
		{"javascript", "javascript:alert(1)", false}, {"data", "data:text/html,evil", false}, {"relative", "/etc/passwd", false}, {"ftp", "ftp://example.com/x", false}, {"userinfo", "https://user@example.com/x", false},
		{"opaque", "https:example.com", false}, {"empty host", "https:///x", false}, {"empty hostname", "https://:443/x", false}, {"control", "https://example.com/\u202ex", false},
		{"too long", "https://example.com/" + strings.Repeat("x", 2049-len("https://example.com/")), false},
		{"boundary", "https://example.com/" + strings.Repeat("x", 2048-len("https://example.com/")), true},
		{"escaped too long", "https://example.com/" + strings.Repeat("é", 2048-len("https://example.com/")), false},
		{"quoted host", `https://example.com"onmouseover="alert(1)`, false},
		{"markup host", "https://<script>/", false},
		{"empty label", "https://example..com/", false},
		{"leading hyphen", "https://-example.com/", false},
		{"long label", "https://" + strings.Repeat("a", 64) + ".com/", false},
		{"ipv4", "https://127.0.0.1:443/", true},
		{"ipv6", "https://[::1]:443/", true},
		{"http", "http://example.com/x", true}, {"https", "https://example.com/x", true}, {"uppercase scheme", "HTTPS://example.com/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items, dropped := sanitizeOutput([]domain.PlanItem{{Topic: "Recursion", Resources: []domain.Resource{{URL: tc.url, ResourceType: "video"}}}}, "visual")
			want := 0
			if tc.keep {
				want = 1
			}
			if len(items[0].Resources) != want || dropped != 1-want {
				t.Fatalf("resources=%+v dropped=%d", items[0].Resources, dropped)
			}
		})
	}
	t.Run("text", func(t *testing.T) {
		items, dropped := sanitizeOutput([]domain.PlanItem{{Explanation: "\x00\n\t\u202e\u200b" + strings.Repeat("é", 1001), Resources: []domain.Resource{{URL: "https://example.com", ResourceType: "Video", Title: "\x7f\u202e\u200d" + strings.Repeat("界", 201)}}}}, "visual")
		if dropped != 0 || items[0].Explanation != strings.Repeat("é", 1000) || items[0].Resources[0].Title != strings.Repeat("界", 200) || items[0].Resources[0].ResourceType != "video" {
			t.Fatalf("text was not bounded or normalized: %+v", items)
		}
	})
	for _, tc := range []struct {
		profile string
		allowed []string
	}{
		{"visual", []string{"video", "diagram", "infographic"}}, {"auditory", []string{"podcast", "lecture", "audio-course"}}, {"read_write", []string{"article", "textbook", "documentation"}}, {"kinesthetic", []string{"exercise", "lab", "coding-challenge"}},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			resources := make([]domain.Resource, 0, 4+len(tc.allowed))
			resources = append(resources, domain.Resource{URL: "https://example.com", ResourceType: "VISUAL"}, domain.Resource{URL: "https://example.com", ResourceType: "unknown"})
			for _, kind := range tc.allowed {
				resources = append(resources, domain.Resource{URL: "https://example.com", ResourceType: strings.ToUpper(kind)})
			}
			resources = append(resources, domain.Resource{URL: "https://example.com", ResourceType: tc.allowed[0]}, domain.Resource{URL: "https://example.com", ResourceType: tc.allowed[1]})
			items, dropped := sanitizeOutput([]domain.PlanItem{{Resources: resources}}, tc.profile)
			kinds := make([]string, 0, len(items[0].Resources))
			for _, resource := range items[0].Resources {
				kinds = append(kinds, resource.ResourceType)
			}
			if !reflect.DeepEqual(kinds, tc.allowed) || dropped != 4 {
				t.Fatalf("types=%v dropped=%d", kinds, dropped)
			}
		})
	}
	t.Run("mismatched type", func(t *testing.T) {
		items, dropped := sanitizeOutput([]domain.PlanItem{{Resources: []domain.Resource{{URL: "https://example.com", ResourceType: "podcast"}}}}, "visual")
		if len(items[0].Resources) != 0 || dropped != 1 {
			t.Fatalf("resources=%+v dropped=%d", items[0].Resources, dropped)
		}
	})
}

func TestOutputTextKeepsWordBoundaries(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"First paragraph.\nSecond paragraph.", "First paragraph. Second paragraph."},
		{"col1\tcol2", "col1 col2"},
		{"First \r\n\v\f\t\u0085 second", "First second"},
		{" \n first\t ", "first"},
		{"a\x00\u202eb\u200bc", "abc"},
		{"می\u200cخواهم 👩\u200d💻", "میخواهم 👩💻"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			items, _ := sanitizeOutput([]domain.PlanItem{{Explanation: tc.input, Resources: []domain.Resource{{URL: "https://example.com", ResourceType: "video", Title: tc.input}}}}, "visual")
			if items[0].Explanation != tc.want || items[0].Resources[0].Title != tc.want {
				t.Fatalf("text=%q title=%q want=%q", items[0].Explanation, items[0].Resources[0].Title, tc.want)
			}
		})
	}
}

func TestCleanTextTruncation(t *testing.T) {
	for _, tc := range []struct {
		input string
		limit int
		want  string
	}{
		{"abcd efgh", 0, ""},
		{"abcd efgh", 4, "abcd"},
		{"abcd efgh", 5, "abcd"},
		{"abcd efgh", 6, "abcd e"},
		{"界界 next", 3, "界界"},
		{"界界 next", 4, "界界 n"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := cleanText(tc.input, tc.limit); got != tc.want {
				t.Fatalf("cleanText(%q, %d)=%q want=%q", tc.input, tc.limit, got, tc.want)
			}
		})
	}
}

func TestOutputURLIsNormalized(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{" https://example.com/x", "https://example.com/x"},
		{"https://example.com/x\n", "https://example.com/x"},
		{`https://example.com/"><script>alert(1)</script>`, "https://example.com/%22%3E%3Cscript%3Ealert%281%29%3C/script%3E"},
		{`https://example.com/?q="><script>alert(1)</script>`, "https://example.com/?q=%22%3E%3Cscript%3Ealert(1)%3C/script%3E"},
		{`https://example.com/?"onmouseover="alert(1)`, "https://example.com/?%22onmouseover=%22alert(1)"},
		{"https://example.com/reader's-guide", "https://example.com/reader%27s-guide"},
		{"https://example.com/?q='onmouseover='alert(1)", "https://example.com/?q=%27onmouseover=%27alert(1)"},
		{"https://example.com/%2freader's-guide", "https://example.com/%2freader%27s-guide"},
		{"https://example.com/reader%27s-guide?q=%27", "https://example.com/reader%27s-guide?q=%27"},
		{"https://example.com/?q=hello world&text=é", "https://example.com/?q=hello%20world&text=%C3%A9"},
		{"https://example.com/?q=AZaz09-._~!$&'()*+,;=:@/?%2f%3C", "https://example.com/?q=AZaz09-._~!$&%27()*+,;=:@/?%2f%3C"},
		{"https://example.com/?q=%x%2%", "https://example.com/?q=%25x%252%25"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			items, dropped := sanitizeOutput([]domain.PlanItem{{Resources: []domain.Resource{{URL: tc.input, ResourceType: "video"}}}}, "visual")
			if dropped != 0 || len(items[0].Resources) != 1 || items[0].Resources[0].URL != tc.want {
				t.Fatalf("resources=%+v dropped=%d want=%q", items[0].Resources, dropped, tc.want)
			}
		})
	}
}
