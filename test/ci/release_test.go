package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReleaseDryRunBuildSelection(t *testing.T) {
	release := loadWorkflow(t, "release.yml")
	verify := release.Jobs["verify"]
	lint := verify.indexWhere(func(s step) bool { return s.runs("make lint test") })
	earlySmoke := verify.indexWhere(func(s step) bool { return s.runs("make docker-build container-smoke") })
	if lint < 0 || verify.Steps[lint].If != "" || earlySmoke <= lint {
		t.Fatal("release verification must run lint and tests before the conditional image smoke")
	}
	if got := verify.Steps[earlySmoke].If; got != "steps.release.outputs.publish != 'false' || steps.release.outputs.native-inventory != 'false'" {
		t.Errorf("early smoke must only skip when both release mode outputs are explicitly false: %q", got)
	}

	dryRun := release.Jobs["dry-run"]
	makeBuild := dryRun.indexWhere(func(s step) bool { return s.Run == "make docker-build-release" })
	actionBuild := dryRun.indexWhere(usesAction("docker/build-push-action@"))
	smoke := dryRun.indexWhere(func(s step) bool { return s.runs("make container-smoke") })
	if makeBuild < 0 || actionBuild < 0 || smoke <= makeBuild || smoke <= actionBuild || dryRun.Steps[smoke].If != "" {
		t.Fatal("either native build path must run before the common image smoke")
	}
	if got := dryRun.Steps[makeBuild].If; got != "matrix.arch == 'amd64' && needs.verify.outputs.native-inventory == 'false'" {
		t.Errorf("Make build must be selected only for the default amd64 image: %q", got)
	}
	if got := dryRun.Steps[actionBuild].If; got != "matrix.arch != 'amd64' || needs.verify.outputs.native-inventory != 'false'" {
		t.Errorf("pinned build action must cover the other native dry-run images: %q", got)
	}
}

func TestReleaseDockerBuildWorkflow(t *testing.T) {
	dryRun := loadWorkflow(t, "release.yml").Jobs["dry-run"]
	makeBuild := dryRun.indexWhere(func(s step) bool { return s.Run == "make docker-build-release" })
	if makeBuild < 0 {
		t.Fatal("the default amd64 dry run must call only the fixed make docker-build-release target")
	}
	build := dryRun.Steps[makeBuild]
	for name, expression := range map[string]string{
		"VERSION":    "${{ github.ref_name }}",
		"VCS_REF":    "${{ github.sha }}",
		"BUILD_DATE": "${{ needs.verify.outputs.created }}",
	} {
		if build.Env[name] != expression || strings.Contains(build.Run, expression) {
			t.Fatalf("release metadata %s must be passed through the environment", name)
		}
	}
}

func TestReleaseSmokeUsesValidatedInventoryMode(t *testing.T) {
	release := loadWorkflow(t, "release.yml")
	for _, name := range []string{"dry-run", "build"} {
		job := release.Jobs[name]
		smoke := job.indexWhere(func(s step) bool { return strings.HasPrefix(s.Run, "make container-smoke") })
		if smoke < 0 || job.Steps[smoke].If != "" {
			t.Errorf("%s must smoke both image variants", name)
			continue
		}
		if got := job.Steps[smoke].Env["RELEASE_NATIVE_INVENTORY"]; got != "${{ needs.verify.outputs.native-inventory }}" {
			t.Errorf("%s smoke test inventory mode = %v, want the validated release output", name, got)
		}
	}
}

func TestReleaseDockerBuildArguments(t *testing.T) {
	scratch := t.TempDir()
	root, err := os.OpenRoot(scratch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := root.WriteFile("docker", []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" >> \"$DOCKER_ARGS_LOG\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.Chmod("docker", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", scratch+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_ARGS_LOG", filepath.Join(scratch, "docker-args"))
	t.Setenv("RUNNER_TEMP", filepath.Join(scratch, "runner temp"))
	t.Setenv("VERSION", `topic/a'"$HOME;check`)
	t.Setenv("VCS_REF", "0123456789abcdef")
	t.Setenv("BUILD_DATE", "2026-11-25T00:00:00Z")
	t.Setenv("IMAGE", "axiom-engine:local")
	t.Setenv("BAML_DIAGNOSTIC", "0")
	_, versionLine, ok := strings.Cut(readRepoFile(t, "Dockerfile"), "ARG BAML_LIB_VERSION=")
	if !ok {
		t.Fatal("the Dockerfile must pin the BAML native library")
	}
	bamlVersion, _, _ := strings.Cut(versionLine, "\n")

	for _, tc := range []struct {
		name string
		cmd  *exec.Cmd
		args []string
	}{
		{"local", exec.CommandContext(t.Context(), "make", "docker-build"), []string{"build", "--target", "runtime"}},
		{"diagnostic", exec.CommandContext(t.Context(), "make", "docker-build", "BAML_DIAGNOSTIC=1"), []string{"build", "--target", "runtime-with-baml-check"}},
		{"release", exec.CommandContext(t.Context(), "make", "docker-build-release"), []string{
			"buildx", "build", "--platform", "linux/amd64", "--load",
			"--output", "type=oci,dest=" + filepath.Join(os.Getenv("RUNNER_TEMP"), "runtime-amd64.tar"),
			"--build-arg", "VERSION=" + os.Getenv("VERSION"),
			"--build-arg", "VCS_REF=" + os.Getenv("VCS_REF"),
			"--build-arg", "BUILD_DATE=" + os.Getenv("BUILD_DATE"),
			"--provenance=mode=max", "--sbom=true", "--target", "runtime",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := root.WriteFile("docker-args", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			tc.cmd.Dir = repoPath()
			if out, err := tc.cmd.CombinedOutput(); err != nil {
				t.Fatalf("docker-build: %v\n%s", err, out)
			}
			data, err := root.ReadFile("docker-args")
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
			want := tc.args
			want = append(want, "--build-arg", "BAML_VERSION="+bamlVersion, "-t", "axiom-engine:local", ".")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("docker arguments = %q, want %q", got, want)
			}
		})
	}
}
