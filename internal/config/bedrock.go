package config

import (
	"fmt"
	"strconv"
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
