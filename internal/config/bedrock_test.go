package config

import (
	"os"
	"testing"
)

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
