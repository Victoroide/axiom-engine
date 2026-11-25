package config

import (
	"runtime"
	"strings"
	"testing"
)

func TestGraphReloadConfig(t *testing.T) {
	for _, load := range []struct {
		name string
		read func(func(string) string) (Config, error)
	}{{"HTTP", Load}, {"MCP", LoadMCP}} {
		t.Run(load.name, func(t *testing.T) {
			var unsupported string
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				unsupported = "supported only"
			}
			for _, tc := range []struct{ mode, path, wantError string }{
				{},
				{path: "curriculum.json"},
				{mode: "false"},
				{mode: "true", wantError: "GRAPH_FILE is required"},
				{mode: "true", path: "curriculum.json", wantError: unsupported},
				{mode: "TRUE", path: "curriculum.json", wantError: "GRAPH_RELOAD_ENABLED must be true or false"},
			} {
				env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "GRAPH_RELOAD_ENABLED": tc.mode, "GRAPH_FILE": tc.path}
				cfg, err := load.read(func(key string) string { return env[key] })
				if tc.wantError == "" {
					if err != nil || cfg.GraphFile != tc.path || cfg.GraphReloadEnabled != (tc.mode == "true") {
						t.Errorf("mode=%q path=%q: config=%+v err=%v", tc.mode, tc.path, cfg, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Errorf("mode=%q path=%q: error=%v want %q", tc.mode, tc.path, err, tc.wantError)
				}
			}
		})
	}
}
