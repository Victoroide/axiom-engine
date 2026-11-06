package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateGraphCommand(t *testing.T) {
	t.Setenv("AXIOM_INTERNAL_TOKEN", "")
	t.Setenv("AXIOM_AUTH_DISABLED", "invalid")
	t.Setenv("AWS_RETRY_MODE", "invalid")
	t.Setenv("BAML_LOG", "invalid")
	var out bytes.Buffer
	if err := run([]string{"validate-graph", "../../internal/graph/seed.json"}, &out); err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Concepts     int
		Relations    map[string]int
		Roots        []string
		LongestChain []string `json:"longest_chain"`
	}
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Concepts != 23 || summary.Relations["depends_on"] != 17 || summary.Relations["is_a"] != 3 || len(summary.Roots) != 4 || len(summary.LongestChain) != 6 {
		t.Fatalf("summary=%s", out.Bytes())
	}
	for _, args := range [][]string{{"validate-graph"}, {"validate-graph", "a", "b"}} {
		if err := run(args, &out); err == nil || !strings.Contains(err.Error(), "usage: axiom-server validate-graph <file>") {
			t.Fatalf("args=%v error=%v", args, err)
		}
	}
}

func TestValidateGraphProcess(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "axiom-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".") //nolint:gosec // Build the server in a fresh test directory.
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`[{"subject":"A","relation":"unknown","object":"B"}]`), 0600); err != nil {
		t.Fatal(err)
	} //nolint:gosec // The destination is inside a fresh test directory.
	for _, tc := range []struct {
		file, message string
		status        int
	}{
		{"../../internal/graph/seed.json", "", 0},
		{"../../examples/graphs/computer-science.json", "", 0},
		{invalid, `tuple 0 has unsupported relation "unknown"`, 1},
		{filepath.Join(dir, "absent.json"), "open graph:", 1},
	} {
		t.Run(tc.file, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), binary, "validate-graph", tc.file) //nolint:gosec // Execute the server built by this test with fixture paths.
			cmd.Env = append(os.Environ(), "AXIOM_INTERNAL_TOKEN=", "AXIOM_AUTH_DISABLED=invalid", "AWS_RETRY_MODE=invalid", "BAML_LOG=off", "PORT=invalid")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			status := 0
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				status = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if status != tc.status {
				t.Fatalf("exit=%d stderr=%s", status, &stderr)
			}
			if tc.status == 0 {
				var summary map[string]any
				if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
					t.Fatal(err)
				}
				if len(summary) != 4 {
					t.Fatal(summary)
				}
				if stderr.Len() != 0 {
					t.Fatalf("stderr=%s", &stderr)
				}
			} else if stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.message) {
				t.Fatalf("stdout=%s stderr=%s", &stdout, &stderr)
			}
		})
	}
}
