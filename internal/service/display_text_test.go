package service

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func TestDisplayText(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"Persian", "می\u200cخواهم", "می\u200cخواهم"},
		{"Arabic nonspacing mark", "بَ\u200cت", "بَ\u200cت"},
		{"Arabic right-joining left letter", "ا\u200cب", "اب"},
		{"Arabic ZWJ", "ب\u200dت", "بت"},
		{"Indic conjunct", "क्\u200dष", "क्\u200dष"},
		{"Indic visible virama", "क्\u200cष", "क्\u200cष"},
		{"Indic explicit half form", "क्\u200d", "क्\u200d"},
		{"Indic mixed script", "क्\u200cষ a्\u200d", "क्ষ a्"},
		{"Sinhala", "ශ්\u200dරී", "ශ්\u200dරී"},
		{"Emoji GB11", "👩\u200d💻 🧑🏽\u200d💻 🏳️\u200d🌈 👩̈\u200d💻", "👩\u200d💻 🧑🏽\u200d💻 🏳️\u200d🌈 👩̈\u200d💻"},
		{"Non-pictographic symbols", "A\u200d💻 👩\u200dA 🇵\u200d🇪 1\u200d2", "A💻 👩A 🇵🇪 12"},
		{"Consecutive or standalone joiners", "\u200dabc\u200c a\u200db a\u200cb 👩\u200d\u200d💻 ب\u200c\u200cت", "abc ab ab 👩💻 بت"},
		{"Control separates context", "👩\u202e\u200d💻 ب\u200c\u200bت क्\u2066\u200dष", "👩💻 بت क्ष"},
		{"Space and controls", "\t  A\r\n\u0085\u00a0\u200b\x00B\u202e\u2067\u2069\u200f\ufeff\x7f   ", "A B"},
		{"Invalid bytes", "\xffA\xc0\x80", "�A��"},
		{"Empty", "\t\u200b\x00\u202e\u2066\u200d  \u200c\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, limit := range []int{maxResourceTitleRunes, maxExplanationRunes} {
				if got := cleanDisplayText(tc.input, limit); got != tc.want {
					t.Errorf("cleanDisplayText(%q, %d) = %q, want %q", tc.input, limit, got, tc.want)
				}
			}
		})
	}
}

func TestDisplayTextCaps(t *testing.T) {
	for _, limit := range []int{maxResourceTitleRunes, maxExplanationRunes} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			for _, cluster := range []string{"é", "👩\u200d💻", "🇵🇪", "क्\u200dष", "각"} {
				t.Run(cluster, func(t *testing.T) {
					fits := strings.Repeat("a", limit-len([]rune(cluster))) + cluster
					if got := cleanDisplayText(fits+"z", limit); got != fits {
						t.Errorf("exact fit returned %q", got)
					}
					before := strings.Repeat("a", limit-len([]rune(cluster))+1)
					if got := cleanDisplayText(before+cluster+"z", limit); got != before {
						t.Errorf("split cluster returned %q", got)
					}
				})
			}
			if got := cleanDisplayText("e"+strings.Repeat("\u0301", limit)+"z", limit); got != "" {
				t.Error("overlong first cluster must yield empty text")
			}
			prefix := strings.Repeat("a", limit-1)
			if got := cleanDisplayText(prefix+" \u0301z", limit); got != prefix {
				t.Error("truncation retained part of a whitespace cluster")
			}
			prefix = strings.Repeat("a", limit-2)
			for _, suffix := range []struct{ name, text string }{
				{"space in cluster", "\u0d4e b"},
				{"non-joiner in cluster", "ب\u200cت"},
			} {
				t.Run(suffix.name, func(t *testing.T) {
					if got := cleanDisplayText(prefix+suffix.text, limit); got != prefix {
						t.Errorf("truncation retained suffix %q, want only the preceding clusters", strings.TrimPrefix(got, prefix))
					}
					fits := strings.Repeat("a", limit-3) + suffix.text
					if got := cleanDisplayText(fits+"z", limit); got != fits {
						t.Errorf("truncation lost a valid later boundary, returned %q", got)
					}
				})
			}
		})
	}
}

func TestDisplayTextKeepsEmptyFields(t *testing.T) {
	items := []domain.PlanItem{{
		Topic:       "Recursion",
		Explanation: "a" + strings.Repeat("\u0301", maxExplanationRunes),
		Resources:   []domain.Resource{{Title: "\u200d\u200c\u2069\t", URL: "https://example.com", ResourceType: "video"}},
	}}
	got, dropped := sanitizeOutput(items, "visual", true)
	if len(got) != 1 || dropped != 0 || got[0].Topic != "Recursion" || got[0].Explanation != "" ||
		len(got[0].Resources) != 1 || got[0].Resources[0].Title != "" {
		t.Fatalf("unexpected empty fields: items=%+v dropped=%d", got, dropped)
	}
}

func TestDisplayGraphemeConformance(t *testing.T) {
	const version = "17.0.0"
	if unicode.Version != version || norm.Version != version {
		t.Fatalf("Unicode tables differ from fixture %s: Go %s, norm %s", version, unicode.Version, norm.Version)
	}
	f, err := os.Open("testdata/GraphemeBreakTest-" + version + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	checked := 0
	for line := 1; scanner.Scan(); line++ {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] == "#" {
			continue
		}
		checkGraphemeFixture(t, line, fields)
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if checked != 766 {
		t.Fatalf("checked %d conformance vectors, want 766", checked)
	}
}

func checkGraphemeFixture(t *testing.T, line int, fields []string) {
	t.Helper()
	runes := make([]rune, 0, len(fields)/2)
	breaks := make([]int, 0, len(fields)/2+1)
	for _, field := range fields {
		switch field {
		case "÷":
			breaks = append(breaks, len(runes))
		case "×":
		default:
			cp, err := strconv.ParseInt(field, 16, 32)
			if err != nil {
				t.Fatalf("line %d: %v", line, err)
			}
			runes = append(runes, rune(cp))
		}
	}
	boundary := 0
	keepAll := func(string) bool { return true }
	for limit := range len(runes) + 1 {
		if boundary+1 < len(breaks) && limit == breaks[boundary+1] {
			boundary++
		}
		want := string(runes[:breaks[boundary]])
		// Check with the full input and with the single code point of
		// lookahead used by the sanitizer.
		full := graphemePrefix(string(runes), limit, keepAll)
		prefix := graphemePrefix(string(runes[:min(limit+1, len(runes))]), limit, keepAll)
		if full != want || prefix != want {
			t.Errorf("line %d, limit %d: full=%q lookahead=%q want=%q", line, limit, full, prefix, want)
		}
	}
}

func BenchmarkStudyDisplayText(b *testing.B) {
	for _, fixture := range []struct{ name, prefix, unit string }{
		{"combining", "é", "\u0301"},
		{"emoji", "", "🧑🏽\u200d💻"},
		{"filtered", "", "\u202e\u200d"},
	} {
		for _, kib := range []int{64, 128} {
			target := kib * 1024
			input := fixture.prefix + strings.Repeat(fixture.unit, (target-len(fixture.prefix))/len(fixture.unit))
			input += strings.Repeat("\x00", target-len(input))
			for _, policy := range []struct {
				name  string
				clean func(string, int) string
			}{{"legacy", cleanText}, {"display", cleanDisplayText}} {
				b.Run(fmt.Sprintf("%s/%dKiB/%s", fixture.name, kib, policy.name), func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						policy.clean(input, maxExplanationRunes)
					}
				})
			}
		}
	}
}
