package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	t.Run("startup parity", func(t *testing.T) {
		t.Setenv("LLM_PROVIDER", "ollama")
		t.Setenv("LLM_MODEL", "local-planner")
		t.Setenv("LLM_BASE_URL", "http://127.0.0.1:11434/v1")
		testGraphLoadParity(t, binary)
	})
}

func testGraphLoadParity(t *testing.T, binary string) {
	t.Helper()
	const tuple = `[{"subject":"Topic","relation":"depends_on","object":"Base"}]`
	const limit = 8 * 1024 * 1024
	for _, tc := range []struct {
		name, suffix string
	}{
		{"at limit", ""},
		{"over limit", " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "curriculum.json")
			data := tuple + strings.Repeat(" ", limit-len(tuple)) + tc.suffix
			if err := os.WriteFile(path, []byte(data), 0600); err != nil { //nolint:gosec // Write a fixture in a fresh test directory.
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cli := exec.CommandContext(ctx, binary, "validate-graph", path) //nolint:gosec // Run the server built by this test with a fixture path.
			cli.Env = graphProcessEnv()
			var stdout, stderr bytes.Buffer
			cli.Stdout, cli.Stderr = &stdout, &stderr
			cliErr := cli.Run()
			startup := exec.CommandContext(ctx, binary, "mcp") //nolint:gosec // Run the server built by this test with a fixture path.
			startup.Env = append(graphProcessEnv(),
				"GRAPH_FILE="+path, "LOG_LEVEL=info",
				"LLM_PROVIDER=aws-bedrock", "AWS_REGION=us-east-1", "AWS_MAX_ATTEMPTS=3", "AWS_RETRY_MODE=standard",
			)
			logs, startupErr := startup.CombinedOutput()
			if tc.suffix == "" {
				if cliErr != nil || startupErr != nil || stderr.Len() != 0 {
					t.Fatalf("CLI error=%v stderr=%q, startup error=%v logs=%s", cliErr, &stderr, startupErr, logs)
				}
				var summary struct{ Relations map[string]int }
				if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil || len(summary.Relations) != 1 || summary.Relations["depends_on"] != 1 {
					t.Fatalf("CLI summary=%s, startup logs=%s", &stdout, logs)
				}
				if err := checkStartupGraph(logs, summary.Relations["depends_on"]); err != nil {
					t.Fatalf("%v, CLI summary=%s, startup logs=%s", err, &stdout, logs)
				}
				return
			}
			var cliExit, startupExit *exec.ExitError
			if !errors.As(cliErr, &cliExit) || cliExit.ExitCode() != 1 || !errors.As(startupErr, &startupExit) || startupExit.ExitCode() != 1 || stdout.Len() != 0 {
				t.Fatalf("CLI exit=%v output=%q error=%q, startup exit=%v logs=%s", cliErr, &stdout, &stderr, startupErr, logs)
			}
			var record struct {
				Message string `json:"msg"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(logs, &record); err != nil || record.Message != "server.failed" || record.Error != strings.TrimSpace(stderr.String()) || !strings.Contains(record.Error, "8388608 bytes") {
				t.Fatalf("CLI error=%q, startup logs=%s", &stderr, logs)
			}
		})
	}
}

func graphProcessEnv() []string {
	env := []string{"BAML_LOG=off"}
	for _, name := range []string{"BAML_LIBRARY_PATH", "BAML_LIBRARY_DISABLE_DOWNLOAD", "HOME", "XDG_CACHE_HOME", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func checkStartupGraph(logs []byte, tuples int) error {
	dec := json.NewDecoder(bytes.NewReader(logs))
	for {
		var record struct {
			Message string `json:"msg"`
			Source  string `json:"graph_source"`
			Tuples  int    `json:"graph_tuples"`
		}
		if err := dec.Decode(&record); err != nil {
			if err == io.EOF {
				return fmt.Errorf("missing server.started record")
			}
			return fmt.Errorf("decode startup log: %w", err)
		}
		if record.Message != "server.started" {
			continue
		}
		if record.Source != "file" || record.Tuples != tuples {
			return fmt.Errorf("startup graph source=%q tuples=%d, want file with %d tuples", record.Source, record.Tuples, tuples)
		}
		return nil
	}
}

func TestCheckStartupGraph(t *testing.T) {
	for _, tc := range []struct {
		name, log string
		wantOK    bool
	}{
		{"file with matching count", `{"msg":"server.started","graph_source":"file","graph_tuples":1}`, true},
		{"file with ten tuples", `{"msg":"server.started","graph_source":"file","graph_tuples":10}`, false},
		{"file with one hundred tuples", `{"msg":"server.started","graph_source":"file","graph_tuples":100}`, false},
		{"embedded graph", `{"msg":"server.started","graph_source":"embedded","graph_tuples":1}`, false},
		{"count in unrelated record", `{"msg":"server.connecting","graph_source":"file","graph_tuples":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkStartupGraph([]byte(tc.log), 1); (err == nil) != tc.wantOK {
				t.Fatalf("checkStartupGraph error=%v, want success=%v", err, tc.wantOK)
			}
		})
	}
}
