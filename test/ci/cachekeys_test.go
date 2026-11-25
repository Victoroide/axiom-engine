package ci_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	dockerfile = `ARG BAML_LIB_VERSION=0.226.3
ARG BAML_LIB_SHA256_AMD64=44ffbd33d0c3b3a79a228a815c657b47c3c0151258da9f62b24d62491467d8ac
ARG BAML_LIB_SHA256_ARM64=32cac207efacbb0776479a1c0b6e02a24c56d2adecb796d00fe38ce71c335d64

FROM golang:1.27.4 AS builder
ARG BAML_LIB_VERSION
RUN go build ./...
`
	makefile = "GOLANGCI_LINT_VERSION := v2.15.0\nIMAGE ?= axiom-engine:local\n"

	fakeGo = `#!/bin/sh
[ "$1" = env ] || exit 2
shift
for name in "$@"; do
  case "$name" in
  GOVERSION) printf '%s\n' "$FAKE_GOVERSION" ;;
  GOMODCACHE) printf '%s\n' "$FAKE_GOMODCACHE" ;;
  GOCACHE) printf '%s\n' "$FAKE_GOCACHE" ;;
  CC) printf '%s\n' "$FAKE_CC" ;;
  *) exit 2 ;;
  esac
done
`
	fakeCompiler = "#!/bin/sh\nprintf '%s\\n' \"$FAKE_CC_VERSION\"\nprintf 'Copyright notice\\n'\n"
	fakeDate     = "#!/bin/sh\nprintf '%s\\n' \"$FAKE_WEEK\"\n"
)

// fixture is a repository root and a PATH of fake tools for one script run.
type fixture struct {
	t    *testing.T
	repo string
	bin  string
	vars map[string]string
}

func newFixture(t *testing.T, changes ...func(*fixture)) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, repo: filepath.Join(root, "repo"), bin: filepath.Join(root, "bin")}
	f.vars = map[string]string{
		"RUNNER_OS":           "Linux",
		"RUNNER_ARCH":         "X64",
		"ImageOS":             "ubuntu24",
		"FAKE_GOVERSION":      "go1.27.4",
		"FAKE_GOMODCACHE":     "/home/runner/go/pkg/mod",
		"FAKE_GOCACHE":        "/home/runner/.cache/go-build",
		"FAKE_CC":             filepath.Join(f.bin, "fake-cc"),
		"FAKE_CC_VERSION":     "gcc (Ubuntu 13.3.0-6ubuntu2~24.04) 13.3.0",
		"FAKE_WEEK":           "2026-W48",
		"GOLANGCI_LINT_CACHE": "/home/runner/work/_temp/golangci-lint",
	}
	f.write("go.mod", "module example.com/axiom\n\ngo 1.27.4\n")
	f.write("go.sum", "example.com/dep v1.0.0 h1:aaaa\n")
	f.write("tools/go.mod", "module example.com/axiom/tools\n\ngo 1.27.4\n")
	f.write("tools/go.sum", "example.com/tool v1.0.0 h1:bbbb\n")
	f.write("Dockerfile", dockerfile)
	f.write("Makefile", makefile)
	f.write(".golangci.yml", "version: \"2\"\n")
	f.tool("go", fakeGo)
	f.tool("fake-cc", fakeCompiler)
	f.tool("date", fakeDate)
	for _, change := range changes {
		change(f)
	}
	return f
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	path := filepath.Join(f.repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) tool(name, content string) {
	f.t.Helper()
	if err := os.MkdirAll(f.bin, 0o750); err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(f.bin, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // The fake tool must be executable.
		f.t.Fatal(err)
	}
}

type result struct {
	outputs  map[string]string
	stdout   string
	stderr   string
	exitCode int
}

func (f *fixture) run(variant ...string) result {
	f.t.Helper()
	script, err := filepath.Abs(repoPath("scripts", "ci-go-cache.sh"))
	if err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", append([]string{script}, variant...)...) //nolint:gosec // Run the repository script with fake tools on PATH.
	cmd.Dir = f.repo
	cmd.Env = []string{"PATH=" + f.bin + ":/usr/bin:/bin", "HOME=" + f.repo}
	for k, v := range f.vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit):
		r.exitCode = exit.ExitCode()
	case runErr != nil:
		f.t.Fatal(runErr)
	default:
		r.outputs = parseOutputs(f.t, r.stdout)
	}
	return r
}

func (f *fixture) mustRun(variant string) map[string]string {
	f.t.Helper()
	r := f.run(variant)
	if r.exitCode != 0 {
		f.t.Fatalf("ci-go-cache.sh %s exited %d: %s", variant, r.exitCode, r.stderr)
	}
	return r.outputs
}

// parseOutputs reads the GITHUB_OUTPUT format strictly: name=value lines and
// name<<DELIMITER blocks, nothing else.
func parseOutputs(t *testing.T, stdout string) map[string]string {
	t.Helper()
	outputs := map[string]string{}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if name, delimiter, ok := strings.Cut(lines[i], "<<"); ok && !strings.Contains(name, "=") {
			var values []string
			for i++; i < len(lines) && lines[i] != delimiter; i++ {
				values = append(values, lines[i])
			}
			if i == len(lines) {
				t.Fatalf("output %q is not terminated by %q", name, delimiter)
			}
			outputs[name] = strings.Join(values, "\n")
			continue
		}
		name, value, ok := strings.Cut(lines[i], "=")
		if !ok || name == "" {
			t.Fatalf("stdout line %q is not a step output", lines[i])
		}
		outputs[name] = value
	}
	return outputs
}

func setVar(key, value string) func(*fixture) {
	return func(f *fixture) { f.vars[key] = value }
}

func appendTo(name, extra string, base string) func(*fixture) {
	return func(f *fixture) { f.write(name, base+extra) }
}

func TestScriptOutputsForEachVariant(t *testing.T) {
	f := newFixture(t)
	modules := f.mustRun("modules")
	test := f.mustRun("test")
	lint := f.mustRun("lint")

	if len(modules) != 2 || modules["modules-path"] != f.vars["FAKE_GOMODCACHE"] {
		t.Errorf("modules outputs = %v, want only modules-key and the module cache path", modules)
	}
	if got := test["build-paths"]; got != f.vars["FAKE_GOCACHE"] {
		t.Errorf("test build paths = %q, want only the Go build cache", got)
	}
	if got, want := lint["build-paths"], f.vars["FAKE_GOCACHE"]+"\n"+f.vars["GOLANGCI_LINT_CACHE"]; got != want {
		t.Errorf("lint build paths = %q, want the Go build cache and the analysis cache %q", got, want)
	}
	for _, name := range []string{"modules-key", "modules-path", "build-key", "build-paths", "build-restore-keys"} {
		if test[name] == "" || lint[name] == "" {
			t.Errorf("%s is empty in test %v or lint %v", name, test[name], lint[name])
		}
	}
	if modules["modules-key"] != test["modules-key"] || modules["modules-key"] != lint["modules-key"] {
		t.Errorf("modules keys differ between jobs: %q, %q, %q; one cache is shared", modules["modules-key"], test["modules-key"], lint["modules-key"])
	}
	if f.mustRun("test")["build-key"] != test["build-key"] {
		t.Error("the same inputs produced a different build key")
	}
}

func TestVariantsDoNotShareBuildEntries(t *testing.T) {
	f := newFixture(t)
	test, lint := f.mustRun("test"), f.mustRun("lint")
	if test["build-key"] == lint["build-key"] {
		t.Fatalf("test and lint share build key %q", test["build-key"])
	}
	for _, p := range strings.Split(test["build-restore-keys"], "\n") {
		if strings.HasPrefix(lint["build-key"], p) {
			t.Errorf("test prefix %q restores the lint entry %q", p, lint["build-key"])
		}
	}
	for _, p := range strings.Split(lint["build-restore-keys"], "\n") {
		if strings.HasPrefix(test["build-key"], p) {
			t.Errorf("lint prefix %q restores the test entry %q", p, test["build-key"])
		}
	}
}

type relation int

const (
	// The change moves the entry to another toolchain or job variant: no earlier
	// entry may be restored by prefix.
	otherToolchain relation = iota
	// The manifests changed: the toolchain-wide prefix still finds the old entry.
	newManifests
	// Only the refresh week changed: both prefixes still find the old entry.
	newWeek
)

func TestKeysFollowTheirInputs(t *testing.T) {
	base := map[string]map[string]string{}
	f := newFixture(t)
	base["test"], base["lint"] = f.mustRun("test"), f.mustRun("lint")

	mutations := []struct {
		name                string
		apply               func(*fixture)
		modules, test, lint bool // whether that entry's key changes
		relation            relation
	}{
		{"runner OS", setVar("RUNNER_OS", "macOS"), true, true, true, otherToolchain},
		{"runner architecture", setVar("RUNNER_ARCH", "ARM64"), true, true, true, otherToolchain},
		{"runner image", setVar("ImageOS", "ubuntu26"), true, true, true, otherToolchain},
		{"Go version", setVar("FAKE_GOVERSION", "go1.27.5"), true, true, true, otherToolchain},
		{"C compiler", setVar("FAKE_CC_VERSION", "gcc (Ubuntu 14.2.0) 14.2.0"), false, true, true, otherToolchain},
		{"native library pin", func(f *fixture) {
			f.write("Dockerfile", strings.Replace(dockerfile, "0.226.3", "0.227.0", 1))
		}, false, true, true, otherToolchain},
		{"Dockerfile outside the pins", appendTo("Dockerfile", "RUN true\n", dockerfile), false, false, false, otherToolchain},
		{"root go.mod", appendTo("go.mod", "require example.com/x v1.0.0\n", "module example.com/axiom\n\ngo 1.27.4\n"), true, true, true, newManifests},
		{"root go.sum", appendTo("go.sum", "example.com/x v1.0.0 h1:cccc\n", "example.com/dep v1.0.0 h1:aaaa\n"), true, true, true, newManifests},
		{"tools go.mod", appendTo("tools/go.mod", "require example.com/y v1.0.0\n", "module example.com/axiom/tools\n\ngo 1.27.4\n"), true, true, true, newManifests},
		{"tools go.sum", appendTo("tools/go.sum", "example.com/y v1.0.0 h1:dddd\n", "example.com/tool v1.0.0 h1:bbbb\n"), true, true, true, newManifests},
		{"golangci-lint version", func(f *fixture) {
			f.write("Makefile", strings.Replace(makefile, "v2.15.0", "v2.16.0", 1))
		}, true, false, true, otherToolchain},
		{"Makefile outside the lint version", appendTo("Makefile", "build:\n\tgo build ./...\n", makefile), false, false, false, otherToolchain},
		{"golangci-lint configuration", func(f *fixture) { f.write(".golangci.yml", "version: \"2\"\nlinters:\n  default: none\n") }, false, false, true, otherToolchain},
		{"refresh week", setVar("FAKE_WEEK", "2026-W49"), false, true, true, newWeek},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, m.apply)
			got := map[string]map[string]string{"test": f.mustRun("test"), "lint": f.mustRun("lint")}

			if got["test"]["modules-key"] != got["lint"]["modules-key"] {
				t.Errorf("modules keys differ between test and lint: %q and %q", got["test"]["modules-key"], got["lint"]["modules-key"])
			}
			if changed := got["test"]["modules-key"] != base["test"]["modules-key"]; changed != m.modules {
				t.Errorf("modules key changed = %v, want %v", changed, m.modules)
			}
			if _, ok := got["test"]["modules-restore-keys"]; ok {
				t.Error("the module cache must not fall back to an older entry")
			}

			for variant, want := range map[string]bool{"test": m.test, "lint": m.lint} {
				oldKey, newKey := base[variant]["build-key"], got[variant]["build-key"]
				if !want {
					if oldKey != newKey {
						t.Errorf("%s build key changed from %q to %q", variant, oldKey, newKey)
					}
					continue
				}
				if oldKey == newKey {
					t.Errorf("%s build key stays %q", variant, oldKey)
					continue
				}
				oldPrefixes := strings.Split(base[variant]["build-restore-keys"], "\n")
				newPrefixes := strings.Split(got[variant]["build-restore-keys"], "\n")
				if len(oldPrefixes) != 2 {
					t.Fatalf("%s restore keys = %q, want an exact-manifest prefix and a toolchain prefix", variant, oldPrefixes)
				}
				switch m.relation {
				case otherToolchain:
					for _, p := range oldPrefixes {
						if strings.HasPrefix(newKey, p) {
							t.Errorf("%s: prefix %q of the old entry restores %q from another toolchain or variant", variant, p, newKey)
						}
					}
					for _, p := range newPrefixes {
						if strings.HasPrefix(oldKey, p) {
							t.Errorf("%s: prefix %q of the new entry restores %q from another toolchain or variant", variant, p, oldKey)
						}
					}
				case newManifests:
					if strings.HasPrefix(newKey, oldPrefixes[0]) {
						t.Errorf("%s: exact-manifest prefix %q matches new manifests in %q", variant, oldPrefixes[0], newKey)
					}
					if !strings.HasPrefix(newKey, oldPrefixes[1]) {
						t.Errorf("%s: toolchain prefix %q does not find %q", variant, oldPrefixes[1], newKey)
					}
				case newWeek:
					for _, p := range oldPrefixes {
						if !strings.HasPrefix(newKey, p) {
							t.Errorf("%s: prefix %q does not find %q after the week changed", variant, p, newKey)
						}
					}
				}
			}
		})
	}
}

func TestRestorePrefixesAreOrderedAndContainTheKey(t *testing.T) {
	f := newFixture(t)
	for _, variant := range []string{"test", "lint"} {
		out := f.mustRun(variant)
		prefixes := strings.Split(out["build-restore-keys"], "\n")
		if len(prefixes) != 2 {
			t.Fatalf("%s: restore keys = %q", variant, prefixes)
		}
		if !strings.HasPrefix(out["build-key"], prefixes[0]) || !strings.HasPrefix(prefixes[0], prefixes[1]) {
			t.Errorf("%s: key %q and prefixes %q are not nested from longest to shortest", variant, out["build-key"], prefixes)
		}
		if !strings.HasSuffix(prefixes[0], "-") || !strings.HasSuffix(prefixes[1], "-") {
			t.Errorf("%s: prefixes %q must end at a segment boundary", variant, prefixes)
		}
		if !strings.HasSuffix(out["build-key"], "-2026-W48") {
			t.Errorf("%s: build key %q does not end with the refresh week", variant, out["build-key"])
		}
	}
}

var cacheKey = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func TestKeysUseOnlySafeCharactersAndStayWithinLimits(t *testing.T) {
	// Each value here would break a key if copied unchanged: the comma separates
	// restore keys, a space and a newline end an input.
	f := newFixture(t,
		setVar("FAKE_GOVERSION", "go1.27.4 X:boringcrypto,nocoverageredesign"),
		setVar("ImageOS", "ubuntu 24,04"),
		setVar("RUNNER_OS", "Linux/Odd"),
		setVar("RUNNER_ARCH", "X 64"),
	)
	for _, variant := range []string{"modules", "test", "lint"} {
		out := f.mustRun(variant)
		keys := []string{out["modules-key"]}
		if variant != "modules" {
			keys = append(keys, out["build-key"])
			keys = append(keys, strings.Split(out["build-restore-keys"], "\n")...)
		}
		for _, key := range keys {
			if !cacheKey.MatchString(key) || len(key) > 512 || strings.Contains(key, "--") {
				t.Errorf("%s: key %q has an unsafe character, an empty segment or exceeds 512 bytes", variant, key)
			}
		}
	}
}

func TestMissingInputsStopTheStep(t *testing.T) {
	tests := []struct {
		name    string
		changes []func(*fixture)
		args    []string
		want    int
		message string
	}{
		{"no variant", nil, nil, 2, "usage"},
		{"unknown variant", nil, []string{"all"}, 2, "usage"},
		{"missing go.sum", []func(*fixture){func(f *fixture) { _ = os.Remove(filepath.Join(f.repo, "go.sum")) }}, []string{"test"}, 1, "go.sum"},
		{"missing tools manifest", []func(*fixture){func(f *fixture) { _ = os.Remove(filepath.Join(f.repo, "tools", "go.mod")) }}, []string{"modules"}, 1, "tools/go.mod"},
		{"no native pins", []func(*fixture){func(f *fixture) { f.write("Dockerfile", "FROM scratch\nARG BAML_LIB_VERSION\n") }}, []string{"test"}, 1, "BAML_LIB"},
		{"no lint version", []func(*fixture){func(f *fixture) { f.write("Makefile", "IMAGE ?= x\n") }}, []string{"modules"}, 1, "GOLANGCI_LINT_VERSION"},
		{"empty Go version", []func(*fixture){setVar("FAKE_GOVERSION", "")}, []string{"modules"}, 1, "GOVERSION"},
		{"empty module cache", []func(*fixture){setVar("FAKE_GOMODCACHE", "")}, []string{"modules"}, 1, "GOMODCACHE"},
		{"empty build cache", []func(*fixture){setVar("FAKE_GOCACHE", "")}, []string{"test"}, 1, "GOCACHE"},
		{"lint without its cache directory", []func(*fixture){func(f *fixture) { delete(f.vars, "GOLANGCI_LINT_CACHE") }}, []string{"lint"}, 1, "GOLANGCI_LINT_CACHE"},
		{"lint without its configuration", []func(*fixture){func(f *fixture) { _ = os.Remove(filepath.Join(f.repo, ".golangci.yml")) }}, []string{"lint"}, 1, ".golangci.yml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newFixture(t, tt.changes...).run(tt.args...)
			if r.exitCode != tt.want {
				t.Fatalf("exit code = %d, want %d (stdout %q, stderr %q)", r.exitCode, tt.want, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stderr, tt.message) {
				t.Errorf("stderr = %q, want it to mention %q", r.stderr, tt.message)
			}
			if strings.Contains(r.stdout, "-key=") {
				t.Errorf("a failed run still printed keys: %q", r.stdout)
			}
		})
	}
}

func TestTestVariantNeedsNoLintDirectory(t *testing.T) {
	f := newFixture(t, func(f *fixture) { delete(f.vars, "GOLANGCI_LINT_CACHE") })
	for _, variant := range []string{"modules", "test"} {
		if r := f.run(variant); r.exitCode != 0 {
			t.Errorf("%s: exit code %d: %s", variant, r.exitCode, r.stderr)
		}
	}
}

func TestMissingCompilerGetsItsOwnEntry(t *testing.T) {
	base := newFixture(t).mustRun("test")
	missing := newFixture(t, setVar("FAKE_CC", "/nonexistent/cc")).mustRun("test")
	if base["build-key"] == missing["build-key"] {
		t.Fatalf("a missing compiler shares the key %q with a real one", base["build-key"])
	}
	if base["modules-key"] != missing["modules-key"] {
		t.Error("the compiler must not change the module cache key")
	}
}

func TestMissingRunnerImageIsNamedAndWarned(t *testing.T) {
	f := newFixture(t, func(f *fixture) { delete(f.vars, "ImageOS") })
	r := f.run("test")
	if r.exitCode != 0 {
		t.Fatalf("exit code %d: %s", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stderr, "::warning::") {
		t.Errorf("stderr = %q, want a workflow warning", r.stderr)
	}
	if !strings.Contains(r.outputs["modules-key"], "-unknown-") {
		t.Errorf("modules key %q does not mark the unknown image", r.outputs["modules-key"])
	}
}
