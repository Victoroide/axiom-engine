package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ValidateAWSRetry checks the Bedrock SDK retry settings before native calls begin.
func ValidateAWSRetry(lookupEnv func(string) (string, bool)) error {
	if value, set := lookupEnv("AWS_MAX_ATTEMPTS"); set {
		attempts, err := strconv.ParseUint(value, 10, 32)
		if err != nil || attempts == 0 {
			return fmt.Errorf("AWS_MAX_ATTEMPTS must be an integer between 1 and 4294967295")
		}
	}
	if value, set := lookupEnv("AWS_RETRY_MODE"); set && value != "standard" && value != "adaptive" {
		return fmt.Errorf("AWS_RETRY_MODE must be standard or adaptive")
	}
	return nil
}

// AWSRegionSource identifies a configured region source without querying instance metadata.
func AWSRegionSource(getenv func(string) string, readFile func(string) ([]byte, error)) string {
	for _, key := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if getenv(key) != "" {
			return key
		}
	}
	path := getenv("AWS_CONFIG_FILE")
	if path == "" {
		path = filepath.Join(getenv("HOME"), ".aws", "config")
	}
	contents, err := readFile(path)
	if err != nil {
		return ""
	}
	profile := getenv("AWS_PROFILE")
	if profile == "" {
		profile = "default"
	}
	selected := false
	for line := range strings.SplitSeq(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			selected = section == "profile "+profile || (profile == "default" && section == "default")
			continue
		}
		if key, value, ok := strings.Cut(line, "="); selected && ok && strings.TrimSpace(key) == "region" && strings.TrimSpace(value) != "" {
			return "profile:" + profile
		}
	}
	return ""
}
