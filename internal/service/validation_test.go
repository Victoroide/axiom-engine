package service

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/graph"
)

func TestUnknownTopicDetailsAreBounded(t *testing.T) {
	g := graph.NewInMemoryGraph()
	for _, tc := range []struct{ name, topic, quoted string }{
		{"64 KiB value", strings.Repeat("x", 64*1024), `"` + strings.Repeat("x", 128) + `"…`},
		{"128 Unicode runes", strings.Repeat("界", 128), `"` + strings.Repeat("界", 128) + `"`},
		{"129 Unicode runes", strings.Repeat("界", 129), `"` + strings.Repeat("界", 128) + `"…`},
		{"control and format characters", "Unknown\t\u200b\u202e\x1bName", `"Unknown\t\u200b\u202e\x1bName"`},
		{"printable Unicode", "Recursión", `"Recursión"`},
		{"invalid UTF-8", "Unknown\xffName", `"Unknown\xffName"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateTopics("failed_topics", []string{tc.topic}, 20, g)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("expected unknown topic validation, got %v", err)
			}
			want := "failed_topics contains unknown topics: " + tc.quoted
			if validation.Details != want {
				t.Fatalf("details (%d bytes) = %.256q; want %q", len(validation.Details), validation.Details, want)
			}
			if !reflect.DeepEqual(validation.UnknownTopics, []string{tc.topic}) {
				t.Fatal("unknown topics lost the original value")
			}
		})
	}
	t.Run("ten displayed values", func(t *testing.T) {
		topics := make([]string, 12)
		for i := range topics {
			topics[i] = fmt.Sprintf("unknown-%02d", i)
		}
		_, err := ValidateTopics("topics", topics, 20, g)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("expected unknown topic validation, got %v", err)
		}
		want := `topics contains unknown topics: "unknown-00", "unknown-01", "unknown-02", "unknown-03", "unknown-04", "unknown-05", "unknown-06", "unknown-07", "unknown-08", "unknown-09" (+2 more)`
		if validation.Details != want || !reflect.DeepEqual(validation.UnknownTopics, topics) {
			t.Fatalf("validation = %+v; want details %q and all unknown topics", validation, want)
		}
	})
}
