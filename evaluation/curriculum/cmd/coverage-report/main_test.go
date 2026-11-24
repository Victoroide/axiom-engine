package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageReportCLIDeterministic(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	manifest := filepath.Join(root, "evaluation/curriculum/editions/ap-csa-fall-2025/manifest.json")
	crosswalk := filepath.Join(root, "evaluation/curriculum/editions/ap-csa-fall-2025/computer-science.crosswalk.json")
	graph := filepath.Join(root, "examples/graphs/computer-science.json")
	args := func(format string) []string {
		return []string{"-manifest", manifest, "-crosswalk", crosswalk, "-graph", graph, "-format", format}
	}

	for _, format := range []string{"json", "markdown"} {
		t.Run(format, func(t *testing.T) {
			var first, second bytes.Buffer
			if err := run(args(format), &first); err != nil {
				t.Fatalf("first run: %v", err)
			}
			if err := run(args(format), &second); err != nil {
				t.Fatalf("second run: %v", err)
			}
			if !bytes.Equal(first.Bytes(), second.Bytes()) {
				t.Fatalf("CLI %s output differed between consecutive runs", format)
			}
		})
	}

	t.Run("unsupported format", func(t *testing.T) {
		var bad bytes.Buffer
		if err := run(args("xml"), &bad); err == nil || !strings.Contains(err.Error(), `unsupported format "xml"`) {
			t.Fatalf("expected unsupported XML format error, got %v", err)
		}
		if bad.Len() != 0 {
			t.Fatal("unsupported format must not write a report")
		}
	})
}
