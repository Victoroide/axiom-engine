package main

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("AXIOM_TEST_RUN_SERVER") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// serverCommand runs the real entry point using the already-linked test binary.
func serverCommand(ctx context.Context, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], args...) //nolint:gosec // Reexecute the current test binary with fixture arguments.
	// Shutdown tests measure process exit as well as the server's drain budget.
	race := strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
	cmd.Env = slices.Clone(env)
	cmd.Env = append(cmd.Env, "AXIOM_TEST_RUN_SERVER=1", "GORACE="+race)
	if dir, ok := os.LookupEnv("GOCOVERDIR"); ok {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	return cmd
}
