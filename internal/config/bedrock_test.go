package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBedrockRegionConfiguration(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(profile, []byte("[profile local]\nregion = eu-west-2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, loader := range []struct {
		name string
		load func(func(string) string) (Config, error)
	}{{"HTTP", Load}, {"MCP", LoadMCP}} {
		t.Run(loader.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, strict, want string
				env                map[string]string
			}{
				{name: "default leaves an unset region to the SDK"},
				{name: "default leaves a configured region to the SDK", env: map[string]string{"AWS_REGION": "us-west-2"}},
				{name: "off leaves a malformed region to the SDK", strict: "false", env: map[string]string{"AWS_REGION": "Bad Region"}},
				{name: "invalid switch", strict: "yes", want: "BEDROCK_REQUIRE_EXPLICIT_REGION"},
				{name: "unset", strict: "true", want: "AWS_REGION"},
				{name: "empty", strict: "true", env: map[string]string{"AWS_REGION": ""}, want: "AWS_REGION"},
				{name: "blank with alternate", strict: "true", env: map[string]string{"AWS_REGION": " \t", "AWS_DEFAULT_REGION": "us-west-2"}, want: "AWS_REGION"},
				{name: "alternate only", strict: "true", env: map[string]string{"AWS_DEFAULT_REGION": "us-west-2"}, want: "AWS_REGION"},
				{name: "profile only", strict: "true", env: map[string]string{"AWS_CONFIG_FILE": profile, "AWS_PROFILE": "local"}, want: "AWS_REGION"},
				{name: "profile miss", strict: "true", env: map[string]string{"AWS_CONFIG_FILE": profile, "AWS_PROFILE": "missing"}, want: "AWS_REGION"},
				{name: "IMDS only", strict: "true", env: map[string]string{"AWS_EC2_METADATA_DISABLED": "false"}, want: "AWS_REGION"},
				{name: "IMDS disabled", strict: "true", env: map[string]string{"AWS_EC2_METADATA_DISABLED": "true"}, want: "AWS_REGION"},
				{name: "explicit", strict: "true", env: map[string]string{"AWS_REGION": "us-west-2"}},
				{name: "explicit with conflicting sources", strict: "true", env: map[string]string{"AWS_REGION": "us-gov-west-1", "AWS_DEFAULT_REGION": "us-west-2", "AWS_CONFIG_FILE": profile, "AWS_PROFILE": "local"}},
				{name: "China partition", strict: "true", env: map[string]string{"AWS_REGION": "cn-north-1"}},
				{name: "new partition", strict: "true", env: map[string]string{"AWS_REGION": "eusc-de-east-1"}},
				{name: "future partition", strict: "true", env: map[string]string{"AWS_REGION": "new-partition-location-12"}},
				{name: "maximum length", strict: "true", env: map[string]string{"AWS_REGION": strings.Repeat("a", 56) + "-east-1"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					getenv := func(key string) string {
						switch key {
						case "AXIOM_AUTH_DISABLED":
							return "true"
						case "BEDROCK_REQUIRE_EXPLICIT_REGION":
							return tc.strict
						default:
							return tc.env[key]
						}
					}
					cfg, err := loader.load(getenv)
					if tc.want != "" {
						if err == nil || !strings.HasPrefix(err.Error(), tc.want+" ") {
							t.Fatalf("expected %s configuration error, got %v", tc.want, err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if tc.strict != "true" && cfg.LLM != DefaultLLM() {
						t.Fatalf("default planner settings changed: %+v", cfg.LLM)
					}
					if tc.strict == "true" && cfg.LLM.BedrockRegion != tc.env["AWS_REGION"] {
						t.Fatalf("validated region = %q, want %q", cfg.LLM.BedrockRegion, tc.env["AWS_REGION"])
					}
				})
			}
		})
	}
}

func TestBedrockRegionRejectsMalformedIdentifiers(t *testing.T) {
	for _, region := range []string{
		" us-east-1", "us-east-1\n", "us-east\t-1", "US-East-1", "us_east_1", "us--east-1", "us-east-x",
		"us-east-1.example.com", "https://us-east-1", "us-east-1/other", "\x00us-east-1", "eu-öst-1", strings.Repeat("a", 60) + "-east-1",
	} {
		t.Run(region, func(t *testing.T) {
			env := map[string]string{"AXIOM_AUTH_DISABLED": "true", "BEDROCK_REQUIRE_EXPLICIT_REGION": "true", "AWS_REGION": region}
			_, err := Load(func(key string) string { return env[key] })
			if err == nil || !strings.HasPrefix(err.Error(), "AWS_REGION ") || strings.Contains(err.Error(), region) {
				t.Fatalf("expected AWS_REGION error without its value, got %v", err)
			}
		})
	}
}

func TestRegionSource(t *testing.T) {
	for _, tc := range []struct {
		name, contents, want, path string
		env                        map[string]string
		missing                    bool
	}{
		{name: "region", env: map[string]string{"AWS_REGION": "ap-south-1"}, want: "AWS_REGION"},
		{name: "default region", env: map[string]string{"AWS_DEFAULT_REGION": "eu-west-1"}, want: "AWS_DEFAULT_REGION"},
		{name: "region precedes default", env: map[string]string{"AWS_REGION": "ap-south-1", "AWS_DEFAULT_REGION": "eu-west-1"}, want: "AWS_REGION"},
		{name: "region precedes profile", env: map[string]string{"AWS_REGION": "ap-south-1", "AWS_PROFILE": "work"}, contents: "[profile work]\nregion = ca-central-1\n", want: "AWS_REGION"},
		{name: "default region precedes profile", env: map[string]string{"AWS_DEFAULT_REGION": "eu-west-1", "AWS_PROFILE": "work"}, contents: "[profile work]\nregion = ca-central-1\n", want: "AWS_DEFAULT_REGION"},
		{name: "default profile", contents: "[default]\nregion = sa-east-1\n", want: "profile:default", path: "/home/example/.aws/config"},
		{name: "explicit default profile", env: map[string]string{"AWS_PROFILE": "default"}, contents: "[default]\nregion = sa-east-1\n", want: "profile:default", path: "/home/example/.aws/config"},
		{name: "prefixed default profile", contents: "[profile default]\nregion = ca-central-1\n", want: "profile:default", path: "/home/example/.aws/config"},
		{name: "named profile", env: map[string]string{"AWS_PROFILE": "work"}, contents: "[default]\nregion = sa-east-1\n[profile work]\nregion = ca-central-1\n", want: "profile:work", path: "/home/example/.aws/config"},
		{name: "named profile has no fallback", env: map[string]string{"AWS_PROFILE": "work"}, contents: "[default]\nregion = sa-east-1\n[profile work]\noutput = json\n", path: "/home/example/.aws/config"},
		{name: "missing named profile", env: map[string]string{"AWS_PROFILE": "work"}, contents: "[default]\nregion = sa-east-1\n", path: "/home/example/.aws/config"},
		{name: "missing file", missing: true, path: "/home/example/.aws/config"},
		{name: "custom file", env: map[string]string{"AWS_CONFIG_FILE": "/etc/aws/config"}, contents: "[default]\nregion=us-east-2\n", want: "profile:default", path: "/etc/aws/config"},
		{name: "comments and whitespace", contents: "  # region=us-west-1\n ; comment\n [default] \n\t; region=us-west-2\n\tregion \t= eu-north-1 \n", want: "profile:default", path: "/home/example/.aws/config"},
		{name: "commented region", contents: "[default]\n# region=us-west-1\n; region=us-west-2\n", path: "/home/example/.aws/config"},
		{name: "empty region", contents: "[default]\nregion = \t\n", path: "/home/example/.aws/config"},
		{name: "region outside profile", contents: "region=us-east-1\n[other]\nregion=us-west-2\n", path: "/home/example/.aws/config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "HOME" {
					return "/home/example"
				}
				return tc.env[key]
			}
			readPath := ""
			readFile := func(path string) ([]byte, error) {
				readPath = path
				if tc.missing {
					return nil, os.ErrNotExist
				}
				return []byte(tc.contents), nil
			}
			if got := AWSRegionSource(getenv, readFile); got != tc.want {
				t.Errorf("source = %q, want %q", got, tc.want)
			}
			if readPath != tc.path {
				t.Errorf("config path = %q, want %q", readPath, tc.path)
			}
		})
	}
}

func TestValidateBAMLLog(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		set, valid  bool
	}{
		{name: "unset", valid: true},
		{name: "off", value: "off", set: true, valid: true},
		{name: "uppercase", value: "OFF", set: true, valid: true},
		{name: "mixed case", value: "Off", set: true, valid: true},
		{name: "error", value: "error", set: true, valid: true},
		{name: "warn", value: "warn", set: true, valid: true},
		{name: "warning", value: "warning", set: true, valid: true},
		{name: "uppercase warning", value: "WARNING", set: true, valid: true},
		{name: "info", value: "info", set: true, valid: true},
		{name: "debug", value: "debug", set: true, valid: true},
		{name: "trace", value: "trace", set: true, valid: true},
		{name: "empty", set: true},
		{name: "leading space", value: " off", set: true},
		{name: "trailing space", value: "off ", set: true},
		{name: "none", value: "none", set: true},
		{name: "unknown", value: "bogus", set: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBAMLLog(func(key string) (string, bool) {
				if key != "BAML_LOG" {
					t.Fatalf("unexpected environment lookup: %s", key)
				}
				return tc.value, tc.set
			})
			if tc.valid {
				if err != nil {
					t.Fatalf("valid value rejected: %v", err)
				}
				return
			}
			if err == nil || err.Error() != "BAML_LOG must be off, error, warn, warning, info, debug or trace" {
				t.Fatalf("expected logging configuration error, got %v", err)
			}
		})
	}
}
