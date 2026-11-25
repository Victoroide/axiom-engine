// Package ci_test checks required checks, Go cache policy, and release build selection.
package ci_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	cacheRestore = "actions/cache/restore@"
	cacheSave    = "actions/cache/save@"
	deriveStepID = "go-cache"
)

type step struct {
	ID   string         `yaml:"id"`
	Name string         `yaml:"name"`
	If   string         `yaml:"if"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	Env  map[string]any `yaml:"env"`
	With map[string]any `yaml:"with"`
}

type job struct {
	Name        string         `yaml:"name"`
	Permissions map[string]any `yaml:"permissions"`
	Steps       []step         `yaml:"steps"`
}

type workflow struct {
	On          map[string]any `yaml:"on"`
	Permissions map[string]any `yaml:"permissions"`
	Jobs        map[string]job `yaml:"jobs"`
}

// ciChecks maps each job of ci.yml to the name of the required check it reports.
var ciChecks = map[string]string{
	"lint":            "Lint",
	"test":            "Test with race detection",
	"generated":       "Check generated code",
	"vulnerabilities": "Check reachable vulnerabilities",
	"image":           "Build service and runtime image",
	"browser":         "Test API docs in browser",
}

// variants is the argument each job passes to the cache script: the build cache
// the job owns, or only the shared module cache.
var variants = map[string]string{
	"lint":            "lint",
	"test":            "test",
	"generated":       "modules",
	"vulnerabilities": "modules",
	"image":           "modules",
	"browser":         "modules",
}

func repoPath(parts ...string) string {
	return filepath.Clean(filepath.Join(append([]string{"..", ".."}, parts...)...))
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	data, err := os.ReadFile(repoPath(parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	var w workflow
	if err := yaml.Unmarshal([]byte(readRepoFile(t, ".github", "workflows", name)), &w); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return w
}

func (s step) runs(command string) bool {
	for line := range strings.SplitSeq(s.Run, "\n") {
		if strings.TrimSpace(line) == command {
			return true
		}
	}
	return false
}

func (s step) with(key string) string {
	v, ok := s.With[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

func (j job) hasCommand(command string) bool {
	for _, s := range j.Steps {
		if s.runs(command) {
			return true
		}
	}
	return false
}

// indexWhere returns the position of the first step that matches, or -1.
func (j job) indexWhere(match func(step) bool) int {
	for i, s := range j.Steps {
		if match(s) {
			return i
		}
	}
	return -1
}

// lastIndexWhere returns the position of the last step that matches, or -1.
func (j job) lastIndexWhere(match func(step) bool) int {
	for i := len(j.Steps) - 1; i >= 0; i-- {
		if match(j.Steps[i]) {
			return i
		}
	}
	return -1
}

func usesAction(prefix string) func(step) bool {
	return func(s step) bool { return strings.HasPrefix(s.Uses, prefix) }
}

func isMakeStep(s step) bool {
	return strings.Contains(s.Run, "make ")
}

func (j job) steps(prefix string) []step {
	var found []step
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, prefix) {
			found = append(found, s)
		}
	}
	return found
}

func output(name string) string {
	return "${{ steps." + deriveStepID + ".outputs." + name + " }}"
}

func TestRequiredCheckNamesAreUnchanged(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	got := map[string]string{}
	for id, j := range ci.Jobs {
		got[id] = j.Name
	}
	if !reflect.DeepEqual(got, ciChecks) {
		t.Fatalf("ci.yml jobs = %v, want %v", got, ciChecks)
	}

	others := []struct{ file, name string }{
		{"codeql.yml", "Analyze Go"},
		{"dependency-review.yml", "Review dependency changes"},
		{"pr-title.yml", "Validate PR title"},
	}
	for _, o := range others {
		found := false
		for _, j := range loadWorkflow(t, o.file).Jobs {
			found = found || j.Name == o.name
		}
		if !found {
			t.Errorf("%s no longer reports the required check %q", o.file, o.name)
		}
	}
}

func TestChecksRunOnPullRequestsAndMain(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	events := make([]string, 0, len(ci.On))
	for event := range ci.On {
		events = append(events, event)
	}
	if len(events) != 2 || ci.On["push"] == nil || !strings.Contains(fmt.Sprint(ci.On["push"]), "main") {
		t.Fatalf("triggers = %v, want push to main and pull_request only", ci.On)
	}
	if _, ok := ci.On["pull_request"]; !ok {
		t.Fatalf("triggers = %v, want pull_request", ci.On)
	}
}

func TestSetupGoDoesNotSaveCaches(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	for id, j := range ci.Jobs {
		setups := j.steps("actions/setup-go@")
		if len(setups) != 1 {
			t.Errorf("%s: %d setup-go steps, want 1 (every job compiles Go)", id, len(setups))
			continue
		}
		if got := setups[0].with("cache"); got != "false" {
			t.Errorf("%s: setup-go cache = %q, want false so only the CI cache steps write caches", id, got)
		}
		if got := setups[0].with("go-version-file"); got != "go.mod" {
			t.Errorf("%s: setup-go go-version-file = %q, want go.mod", id, got)
		}
	}
}

func TestEveryJobDerivesKeysAndRestoresTheSharedModuleCache(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	for id, j := range ci.Jobs {
		derive := j.indexWhere(func(s step) bool { return s.ID == deriveStepID })
		if derive < 0 {
			t.Errorf("%s: no step with id %q", id, deriveStepID)
			continue
		}
		wantRun := fmt.Sprintf(`./scripts/ci-go-cache.sh %s >> "$GITHUB_OUTPUT"`, variants[id])
		if !j.Steps[derive].runs(wantRun) {
			t.Errorf("%s: derive step runs %q, want %q", id, j.Steps[derive].Run, wantRun)
		}
		setup := j.indexWhere(usesAction("actions/setup-go@"))
		if setup < 0 || derive < setup {
			t.Errorf("%s: keys are derived before Go is set up (needs go env)", id)
		}

		var modules []step
		for _, s := range j.steps(cacheRestore) {
			if s.with("path") == output("modules-path") {
				modules = append(modules, s)
			}
		}
		if len(modules) != 1 {
			t.Errorf("%s: %d module cache restore steps, want 1", id, len(modules))
			continue
		}
		restore := modules[0]
		if got := restore.with("key"); got != output("modules-key") {
			t.Errorf("%s: module cache key = %q, want the shared %q", id, got, output("modules-key"))
		}
		if _, ok := restore.With["restore-keys"]; ok {
			t.Errorf("%s: the module cache accumulates entries and must restore an exact key only", id)
		}
	}
}

func TestOneJobWritesTheModuleCache(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	var writers []string
	for id, j := range ci.Jobs {
		for _, s := range j.steps(cacheSave) {
			if s.with("path") == output("modules-path") {
				writers = append(writers, id)
			}
		}
	}
	if !reflect.DeepEqual(writers, []string{"lint"}) {
		t.Fatalf("module cache writers = %v, want only lint (the job that downloads every module)", writers)
	}
}

func TestTestAndLintOwnSeparateBuildCaches(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	for id, j := range ci.Jobs {
		var restores, saves int
		for _, s := range j.Steps {
			if s.With == nil || s.with("path") != output("build-paths") {
				continue
			}
			switch {
			case strings.HasPrefix(s.Uses, cacheRestore):
				restores++
				if s.with("key") != output("build-key") || s.with("restore-keys") != output("build-restore-keys") {
					t.Errorf("%s: build cache restore uses key %q and restore-keys %q", id, s.with("key"), s.with("restore-keys"))
				}
			case strings.HasPrefix(s.Uses, cacheSave):
				saves++
				if s.with("key") != output("build-key") {
					t.Errorf("%s: build cache save uses key %q", id, s.with("key"))
				}
			}
		}
		want := 0
		if variants[id] != "modules" {
			want = 1
		}
		if restores != want || saves != want {
			t.Errorf("%s: %d build cache restores and %d saves, want %d each", id, restores, saves, want)
		}
	}
}

func TestLintKeepsTheAnalysisCacheInOneKnownDirectory(t *testing.T) {
	lint := loadWorkflow(t, "ci.yml").Jobs["lint"]
	place := lint.indexWhere(func(s step) bool {
		return s.runs(`echo "GOLANGCI_LINT_CACHE=$RUNNER_TEMP/golangci-lint" >> "$GITHUB_ENV"`)
	})
	derive := lint.indexWhere(func(s step) bool { return s.ID == deriveStepID })
	if place < 0 || derive < 0 || place > derive {
		t.Fatalf("lint must set GOLANGCI_LINT_CACHE before deriving keys (set at %d, derived at %d)", place, derive)
	}
}

func TestCachesAreRestoredBeforeAndSavedAfterTheChecks(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	for id, j := range ci.Jobs {
		firstWork := j.indexWhere(isMakeStep)
		lastWork := j.lastIndexWhere(isMakeStep)
		for i, s := range j.Steps {
			switch {
			case strings.HasPrefix(s.Uses, cacheRestore) && i > firstWork:
				t.Errorf("%s: restore step %q runs after the first make command", id, s.Name)
			case strings.HasPrefix(s.Uses, cacheSave) && i < lastWork:
				t.Errorf("%s: save step %q runs before the last make command, so a later failure would not stop it", id, s.Name)
			}
		}
	}
}

// comparison matches one clause of a step condition: a context value compared
// with a string literal.
var comparison = regexp.MustCompile(`^([A-Za-z0-9_.-]+) (==|!=) '([^']*)'$`)

// evaluate runs the restricted conditions this workflow uses: comparisons
// joined by &&. Any other syntax fails the test so the guard stays analyzable.
func evaluate(t *testing.T, expr string, context map[string]string) bool {
	t.Helper()
	result := true
	for clause := range strings.SplitSeq(expr, "&&") {
		m := comparison.FindStringSubmatch(strings.TrimSpace(clause))
		if m == nil {
			t.Fatalf("unsupported clause %q in condition %q", clause, expr)
		}
		value, ok := context[m[1]]
		if !ok {
			t.Fatalf("condition %q reads %q, which the test does not model", expr, m[1])
		}
		if (m[2] == "==") != (value == m[3]) {
			result = false
		}
	}
	return result
}

// statusFunction matches the functions that change when a step runs after a
// failure. The pattern accepts both spellings of the cancellation function.
var statusFunction = regexp.MustCompile(`\b(always|failure|cancel+ed|success)\(`)

func TestCachesAreSavedOnlyAfterASuccessfulPushToMain(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	var saves int
	for id, j := range ci.Jobs {
		for _, s := range j.steps(cacheSave) {
			saves++
			if s.If == "" || statusFunction.MatchString(s.If) {
				t.Errorf("%s: save step %q has condition %q; it must use the implicit success() so a failed check saves nothing", id, s.Name, s.If)
				continue
			}
			var restore *step
			for _, r := range j.steps(cacheRestore) {
				if r.with("key") == s.with("key") && r.with("path") == s.with("path") {
					restore = &r
				}
			}
			if restore == nil || restore.ID == "" {
				t.Errorf("%s: save step %q has no restore step with the same key and path", id, s.Name)
				continue
			}
			hit := "steps." + restore.ID + ".outputs.cache-hit"

			for _, event := range []string{"push", "pull_request", "pull_request_target", "workflow_dispatch", "schedule"} {
				for _, ref := range []string{"refs/heads/main", "refs/heads/main-old", "refs/heads/feature", "refs/pull/241/merge", "refs/tags/v0.1.1"} {
					for _, result := range []string{"", "false", "true"} {
						context := map[string]string{"github.event_name": event, "github.ref": ref, hit: result}
						want := event == "push" && ref == "refs/heads/main" && result != "true"
						if got := evaluate(t, s.If, context); got != want {
							t.Errorf("%s: %q on %s of %s with cache-hit %q = %v, want %v", id, s.Name, event, ref, result, got, want)
						}
					}
				}
			}
		}
	}
	if saves != 3 {
		t.Fatalf("%d cache save steps, want 3: shared modules from lint, one build cache each from test and lint", saves)
	}
}

func TestNoCheckDependsOnCacheState(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	for id, j := range ci.Jobs {
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/cache@") {
				t.Errorf("%s: %q uses the combined cache action, which saves after any event; restore and save explicitly", id, s.Name)
			}
			if strings.HasPrefix(s.Uses, cacheRestore) {
				for _, key := range []string{"fail-on-cache-miss", "lookup-only", "enableCrossOsArchive"} {
					if _, ok := s.With[key]; ok {
						t.Errorf("%s: restore step %q sets %s, so a cold or evicted cache could skip or fail a check", id, s.Name, key)
					}
				}
				continue
			}
			if strings.HasPrefix(s.Uses, cacheSave) {
				continue
			}
			if strings.Contains(s.If, "cache") || strings.Contains(s.If, deriveStepID) {
				t.Errorf("%s: step %q is conditional on cache state: %q", id, s.Name, s.If)
			}
		}
	}
}

func TestImageJobDoesNotRepeatTheHostBuild(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	image := ci.Jobs["image"]
	for _, s := range image.Steps {
		for line := range strings.SplitSeq(s.Run, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "make" && fields[1] == "build" {
				t.Errorf("image job still runs %q; Docker compiles ./cmd/server for the runtime image", strings.TrimSpace(line))
			}
		}
	}

	codeql := loadWorkflow(t, "codeql.yml")
	if !codeql.Jobs["analyze"].hasCommand("make build") {
		t.Error("the required CodeQL job no longer runs make build, which is why the image job may skip it")
	}
	for _, event := range []string{"push", "pull_request"} {
		if _, ok := codeql.On[event]; !ok {
			t.Errorf("CodeQL does not run on %s", event)
		}
	}

	// The smoke script builds its Bedrock fixture with the host toolchain.
	if len(image.steps("actions/setup-go@")) != 1 {
		t.Error("the image job needs Go for the smoke fixture")
	}
	if !image.hasCommand("make container-smoke container-smoke-test IMAGE=axiom-ci") {
		t.Error("the image job must still exercise the built image with both smoke targets")
	}

	buildx := image.steps("docker/setup-buildx-action@")
	if len(buildx) != 1 {
		t.Fatalf("%d buildx setup steps, want 1", len(buildx))
	}
	if _, ok := buildx[0].With["cleanup"]; ok {
		t.Error("the buildx setup must keep its default cleanup of the builder")
	}

	builds := image.steps("docker/build-push-action@")
	if len(builds) != 1 {
		t.Fatalf("%d image build steps, want 1", len(builds))
	}
	build := builds[0]
	for key, want := range map[string]string{
		"context":    ".",
		"target":     "runtime",
		"tags":       "axiom-ci",
		"load":       "true",
		"cache-from": "type=gha,scope=ci-runtime",
		"cache-to":   "${{ github.event_name == 'push' && 'type=gha,mode=max,scope=ci-runtime' || '' }}",
	} {
		if got := build.with(key); got != want {
			t.Errorf("image build %s = %q, want %q", key, got, want)
		}
	}
}

func TestChecksKeepTheirCommands(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	commands := map[string][]string{
		"lint":            {"make lint", "make diagrams-check"},
		"test":            {"make native-baml", "make cover"},
		"generated":       {"make native-baml", "make tools-baml generate-check tidy-check toolchain-check"},
		"vulnerabilities": {"make vuln"},
		"image":           {"make container-smoke container-smoke-test IMAGE=axiom-ci"},
		"browser":         {"make native-baml", "make test-e2e"},
	}
	for id, want := range commands {
		j := ci.Jobs[id]
		last := -1
		for _, command := range want {
			i := j.indexWhere(func(s step) bool { return s.runs(command) })
			if i < 0 {
				t.Errorf("%s: no step runs %q", id, command)
			} else if i < last {
				t.Errorf("%s: %q runs out of order", id, command)
			}
			last = max(last, i)
		}
	}

	upload := ci.Jobs["test"].steps("actions/upload-artifact@")
	if len(upload) != 1 || upload[0].with("name") != "coverage" || upload[0].with("path") != "coverage.out" ||
		upload[0].with("if-no-files-found") != "error" {
		t.Errorf("the test job must still upload coverage.out as the coverage artifact: %+v", upload)
	}
	failures := ci.Jobs["browser"].steps("actions/upload-artifact@")
	if len(failures) != 1 || failures[0].with("name") != "browser-failure" || failures[0].If != "failure()" {
		t.Errorf("the browser job must still keep failure artifacts: %+v", failures)
	}
}

func TestMakeRecipesKeepTheirCoverage(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	for _, line := range []string{
		"\tCGO_ENABLED=1 go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...\n",
		"\tgo vet ./...\n",
		"\tgo vet -tags e2e ./...\n",
		"\tgo -C tools vet ./...\n",
		"\tgo run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...\n",
		"\tgo -C tools run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --config ../.golangci.yml ./...\n",
		"\tgo tool -modfile=tools/go.mod actionlint\n",
		"\tshellcheck scripts/ci-go-cache.sh\n",
	} {
		if !strings.Contains(makefile, line) {
			t.Errorf("Makefile is missing %q", strings.TrimSpace(line))
		}
	}
}

var pinned = regexp.MustCompile(`@[0-9a-f]{40}( |$)`)

func TestWorkflowPostureIsPreserved(t *testing.T) {
	ci := loadWorkflow(t, "ci.yml")
	if !reflect.DeepEqual(ci.Permissions, map[string]any{"contents": "read"}) {
		t.Errorf("workflow permissions = %v, want contents: read", ci.Permissions)
	}
	for id, j := range ci.Jobs {
		if j.Permissions != nil {
			t.Errorf("%s: job permissions %v widen the read-only default", id, j.Permissions)
		}
		checkouts := j.steps("actions/checkout@")
		if len(checkouts) != 1 || checkouts[0].with("persist-credentials") != "false" {
			t.Errorf("%s: checkout must run once with persist-credentials: false", id)
		}
		for _, s := range j.Steps {
			if s.Uses != "" && !strings.HasPrefix(s.Uses, "./") && !pinned.MatchString(s.Uses+" ") {
				t.Errorf("%s: %q is not pinned to a full commit", id, s.Uses)
			}
		}
	}
}

func TestCacheScriptIsExecutable(t *testing.T) {
	info, err := os.Stat(repoPath("scripts", "ci-go-cache.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("scripts/ci-go-cache.sh must be executable; the workflow runs it directly")
	}
}
