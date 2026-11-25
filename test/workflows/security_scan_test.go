package workflows_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	securityScanWorkflow = ".github/workflows/security-scan.yml"
	fuzzTime             = "4m"
	fuzzArtifactPrefix   = "fuzz-crashers-"
)

var (
	pinnedAction   = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	leftoverExpr   = regexp.MustCompile(`\$\{\{`)
	goTestCommand  = regexp.MustCompile(`^go test \S+ -run='\^\$' -fuzz='\^\w+\$' -fuzztime=\S+$`)
	fixtureCommand = regexp.MustCompile(`^go test \. -run='\^\$' -fuzz='(\^Fuzz\w+\$)' -fuzztime=` + fuzzTime + `$`)
)

type workflowFile struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]string
	Jobs        map[string]job
}

type job struct {
	Name           string
	RunsOn         string `yaml:"runs-on"`
	TimeoutMinutes int    `yaml:"timeout-minutes"`
	Permissions    map[string]string
	Strategy       struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct {
			Include []matrixEntry
		}
	}
	Steps []step
}

type matrixEntry struct {
	Package string
	Target  string
}

type step struct {
	Name string
	If   string
	Uses string
	Run  string
	With map[string]any
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root not found at %s: %v", root, err)
	}
	return root
}

func loadWorkflow(t *testing.T) workflowFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), securityScanWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	var wf workflowFile
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", securityScanWorkflow, err)
	}
	return wf
}

func fuzzJob(t *testing.T) job {
	t.Helper()
	j, ok := loadWorkflow(t).Jobs["fuzz"]
	if !ok {
		t.Fatal("security scan has no fuzz job")
	}
	return j
}

func (j job) step(t *testing.T, match func(step) bool, what string) step {
	t.Helper()
	var found []step
	for _, s := range j.Steps {
		if match(s) {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("fuzz job has %d %s steps, want exactly one", len(found), what)
	}
	return found[0]
}

func (j job) fuzzStep(t *testing.T) step {
	return j.step(t, func(s step) bool { return strings.Contains(s.Run, "-fuzz=") }, "fuzz command")
}

func (j job) uploadStep(t *testing.T) step {
	return j.step(t, func(s step) bool { return strings.HasPrefix(s.Uses, "actions/upload-artifact@") }, "artifact upload")
}

func (e matrixEntry) render(t *testing.T, template string) string {
	t.Helper()
	out := strings.NewReplacer(
		"${{ matrix.package }}", e.Package,
		"${{ matrix.target }}", e.Target,
	).Replace(template)
	if leftoverExpr.MatchString(out) {
		t.Fatalf("%q still holds an expression after substituting the matrix entry %+v", out, e)
	}
	return out
}

func TestFuzzMatrixMatchesMakeTarget(t *testing.T) {
	j := fuzzJob(t)
	run := j.fuzzStep(t).Run

	fromWorkflow := make([]string, 0, len(j.Strategy.Matrix.Include))
	seen := map[matrixEntry]bool{}
	for _, entry := range j.Strategy.Matrix.Include {
		if seen[entry] {
			t.Errorf("matrix lists %+v twice", entry)
		}
		seen[entry] = true
		fromWorkflow = append(fromWorkflow, strings.TrimSpace(entry.render(t, run)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "--no-print-directory", "-n", "fuzz", "FUZZTIME="+fuzzTime)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("make -n fuzz: %v", err)
	}
	var fromMake []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			fromMake = append(fromMake, line)
		}
	}

	for _, line := range fromMake {
		if !goTestCommand.MatchString(line) {
			t.Fatalf("make fuzz prints %q, which is not a single go test fuzz command", line)
		}
	}
	sort.Strings(fromMake)
	sort.Strings(fromWorkflow)
	if strings.Join(fromMake, "\n") != strings.Join(fromWorkflow, "\n") {
		t.Fatalf("matrix commands differ from make -n fuzz FUZZTIME=%s\nworkflow:\n  %s\nmake:\n  %s",
			fuzzTime, strings.Join(fromWorkflow, "\n  "), strings.Join(fromMake, "\n  "))
	}
}

func TestFuzzMatrixMatchesMakeTargetUnderParentMake(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "--print-directory", "-f", "-")
	cmd.Env = append(os.Environ(), "MAKEFLAGS=", "MFLAGS=", "FUZZ_TEST_BINARY="+binary)
	cmd.Stdin = strings.NewReader("check:\n\t@\"$$FUZZ_TEST_BINARY\" -test.run '^TestFuzzMatrixMatchesMakeTarget$$'\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fuzz matrix check under parent make: %v\n%s", err, out)
	}
}

func TestFuzzJobKeepsSecurityGuards(t *testing.T) {
	wf := loadWorkflow(t)

	triggers := make([]string, 0, len(wf.On))
	for name := range wf.On {
		triggers = append(triggers, name)
	}
	sort.Strings(triggers)
	if got := strings.Join(triggers, ","); got != "schedule,workflow_dispatch" {
		t.Errorf("triggers = %s, want schedule,workflow_dispatch", got)
	}
	if got := wf.Permissions; len(got) != 1 || got["contents"] != "read" {
		t.Errorf("workflow permissions = %v, want contents: read", got)
	}
	for _, id := range []string{"dependencies", "tools", "image"} {
		scan, ok := wf.Jobs[id]
		if !ok {
			t.Errorf("security scan lost the %s job", id)
			continue
		}
		if scan.Permissions["security-events"] != "write" {
			t.Errorf("%s job permissions = %v, want security-events: write for SARIF upload", id, scan.Permissions)
		}
	}

	j := wf.Jobs["fuzz"]
	if j.Strategy.FailFast == nil || *j.Strategy.FailFast {
		t.Error("fuzz job must set fail-fast: false so one failing target does not cancel the others")
	}
	if j.TimeoutMinutes != 30 {
		t.Errorf("timeout-minutes = %d, want 30", j.TimeoutMinutes)
	}
	if j.RunsOn != "ubuntu-latest" {
		t.Errorf("runs-on = %q, want ubuntu-latest", j.RunsOn)
	}
	if got := j.Permissions; len(got) != 1 || got["contents"] != "read" {
		t.Errorf("fuzz job permissions = %v, want only contents: read", got)
	}
	if j.Name == "" || !strings.Contains(j.Name, "${{ matrix.target }}") {
		t.Errorf("job name %q must include the target so each run is identifiable", j.Name)
	}

	checkout := j.step(t, func(s step) bool { return strings.HasPrefix(s.Uses, "actions/checkout@") }, "checkout")
	if checkout.With["persist-credentials"] != false {
		t.Errorf("checkout with = %v, want persist-credentials: false", checkout.With)
	}
	for _, s := range j.Steps {
		if s.Uses != "" && !pinnedAction.MatchString(s.Uses) {
			t.Errorf("action %q is not pinned to a full commit SHA", s.Uses)
		}
	}
}

func TestFuzzCrasherArtifactsAreUniquePerTarget(t *testing.T) {
	j := fuzzJob(t)
	upload := j.uploadStep(t)
	if upload.If != "${{ failure() }}" {
		t.Errorf("upload condition = %q, want ${{ failure() }}", upload.If)
	}
	if got := fmt.Sprint(upload.With["retention-days"]); got != "30" {
		t.Errorf("retention-days = %s, want 30", got)
	}

	root := repoRoot(t)
	if len(j.Strategy.Matrix.Include) == 0 {
		t.Fatal("fuzz job has an empty matrix, so no target uploads a crasher artifact")
	}
	names := map[string]matrixEntry{}
	for _, entry := range j.Strategy.Matrix.Include {
		name := entry.render(t, fmt.Sprint(upload.With["name"]))
		if name != fuzzArtifactPrefix+entry.Target {
			t.Errorf("artifact for %s is named %q, want %q", entry.Target, name, fuzzArtifactPrefix+entry.Target)
		}
		if other, dup := names[name]; dup {
			t.Errorf("artifact name %q is shared by %+v and %+v", name, other, entry)
		}
		names[name] = entry

		wantPath := entry.Package + "/testdata/fuzz/" + entry.Target + "/"
		if got := entry.render(t, fmt.Sprint(upload.With["path"])); got != wantPath {
			t.Errorf("artifact path for %s = %q, want %q", entry.Target, got, wantPath)
		}
		if !declaresFuzzTarget(t, filepath.Join(root, entry.Package), entry.Target) {
			t.Errorf("%s declares no fuzz target %s", entry.Package, entry.Target)
		}
	}
}

func declaresFuzzTarget(t *testing.T, dir, target string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	signature := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(target) + `\(\w+ \*testing\.F\)`)
	for _, file := range files {
		src, err := os.ReadFile(file) //nolint:gosec // Reads test files matched inside the package directory.
		if err != nil {
			t.Fatal(err)
		}
		if signature.Match(src) {
			return true
		}
	}
	return false
}

// writeFixture creates a throwaway module outside the repository so a fuzz
// failure cannot touch the checked-in seed corpus.
func writeFixture(t *testing.T, source string) string {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "fixture-")
	if err != nil {
		t.Fatal(err)
	}
	// A unique import path isolates the fuzz corpus while sharing the Go build cache.
	module := "example.com/" + filepath.Base(dir)
	for name, body := range map[string]string{
		"go.mod":          "module " + module + "\n\ngo 1.21\n",
		"fixture_test.go": source,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFuzzFixturesHaveSeparateCorpusNamespaces(t *testing.T) {
	modules := make(map[string]bool)
	for _, source := range []string{failingFixture, seedLoggingFixture, seedLoggingFixture} {
		path := filepath.Join(writeFixture(t, source), "go.mod")
		module, err := os.ReadFile(path) //nolint:gosec // Reads the module declaration written by writeFixture.
		if err != nil {
			t.Fatal(err)
		}
		// Go keys its fuzz corpus cache by import path and fuzz target name.
		declaration, _, _ := strings.Cut(string(module), "\n")
		if modules[declaration] {
			t.Fatalf("fixtures running FuzzFixture share the same corpus namespace (%s)", declaration)
		}
		modules[declaration] = true
	}
}

func TestFuzzFixtureRejectsShellSyntax(t *testing.T) {
	const command = "go test . -run='^$' -fuzz='^FuzzFixture$' -fuzztime=4m"
	for _, suffix := range []string{"; printf unexpected > marker", "\nprintf unexpected > marker", "$(printf unexpected > marker)"} {
		dir := t.TempDir()
		out, err := runFuzzCommand(t, dir, command+suffix, fuzzTime)
		if _, err := os.Stat(filepath.Join(dir, "marker")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("workflow command %q created a marker (stat error: %v)", suffix, err)
		}
		if err == nil || !strings.Contains(err.Error(), "unsupported fuzz command") {
			t.Errorf("workflow command %q returned %v, want unsupported fuzz command\n%s", suffix, err, out)
		}
	}
}

func runFuzzCommand(t *testing.T, dir, command, budget string, extraEnv ...string) (string, error) {
	t.Helper()
	match := fixtureCommand.FindStringSubmatch(strings.TrimSpace(command))
	if match == nil {
		return "", fmt.Errorf("unsupported fuzz command %q", command)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", ".", "-run=^$", "-fuzz="+match[1], "-fuzztime="+budget) //nolint:gosec // The workflow supplies only a validated fuzz target pattern.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"GOFLAGS=", "GOWORK=off"}, extraEnv...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

const failingFixture = `package fixture

import "testing"

func FuzzFixture(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, s string) {
		if s != "seed" {
			t.Fatalf("rejected %q", s)
		}
	})
}
`

func TestFuzzFailureFailsJobAndWritesReproducerToUploadedPath(t *testing.T) {
	t.Parallel()
	j := fuzzJob(t)
	entry := matrixEntry{Package: ".", Target: "FuzzFixture"}
	dir := writeFixture(t, failingFixture)

	out, err := runFuzzCommand(t, dir, entry.render(t, j.fuzzStep(t).Run), fuzzTime)
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("fuzz command error = %v, want a non-zero exit so the job fails\n%s", err, out)
	}

	artifactDir := filepath.Join(dir, entry.render(t, fmt.Sprint(j.uploadStep(t).With["path"])))
	reproducers, err := os.ReadDir(artifactDir)
	if err != nil || len(reproducers) == 0 {
		t.Fatalf("no reproducer under the uploaded path %s (err=%v)\n%s", artifactDir, err, out)
	}
	body, err := os.ReadFile(filepath.Join(artifactDir, reproducers[0].Name())) //nolint:gosec // Reads the reproducer the fuzz run just wrote in the temporary module.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "go test fuzz v1\n") {
		t.Fatalf("reproducer %s is not a Go fuzz corpus entry: %q", reproducers[0].Name(), body)
	}
	if name := entry.render(t, fmt.Sprint(j.uploadStep(t).With["name"])); name != "fuzz-crashers-FuzzFixture" {
		t.Fatalf("artifact name = %q, want fuzz-crashers-FuzzFixture", name)
	}
}

const seedLoggingFixture = `package fixture

import (
	"os"
	"testing"
)

func FuzzFixture(f *testing.F) {
	f.Add("seed")
	f.Fuzz(func(t *testing.T, s string) {
		log, err := os.OpenFile(os.Getenv("FUZZ_FIXTURE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		input := "M"
		if s == "seed" {
			input = "S"
		}
		if _, err := log.WriteString(input); err != nil {
			t.Fatal(err)
		}
	})
}
`

func TestFuzzCommandReplaysSeedCorpusAndMutates(t *testing.T) {
	t.Parallel()
	j := fuzzJob(t)
	entry := matrixEntry{Package: ".", Target: "FuzzFixture"}
	dir := writeFixture(t, seedLoggingFixture)
	logPath := filepath.Join(dir, "inputs.log")

	// Limit executions so slow workers still have time to finish the baseline and mutate inputs.
	out, err := runFuzzCommand(t, dir, entry.render(t, j.fuzzStep(t).Run), "100x", "FUZZ_FIXTURE_LOG="+logPath)
	if err != nil {
		t.Fatalf("passing fixture failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(logPath) //nolint:gosec // Reads the log inside the temporary module.
	if err != nil {
		t.Fatalf("fixture never ran: %v\n%s", err, out)
	}
	if !bytes.Contains(raw, []byte("S")) {
		t.Fatalf("the seed corpus entry did not run\n%s", out)
	}
	if !bytes.Contains(raw, []byte("M")) {
		t.Fatalf("no mutated input ran within the budget\n%s", out)
	}
}
