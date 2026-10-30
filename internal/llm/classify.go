package llm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/service"
)

var (
	bamlStatus    = regexp.MustCompile(`(?:^|failed with )status code: (?:[a-z]+ \(|unspecified error code: |badresponse )([0-9]+)\b`)
	bamlException = regexp.MustCompile(`(?m)^[ \t]*source: ([a-z]+)\(`)
)

func classify(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	header, _, _ := strings.Cut(message, "\n")
	var class error
	if status := bamlStatus.FindStringSubmatch(header); len(status) == 2 {
		code, _ := strconv.Atoi(status[1])
		switch {
		case code == 429:
			class = service.ErrThrottled
		case code == 408, code >= 500 && code <= 599, code == 2:
			class = service.ErrUpstreamUnavailable
		case code >= 400 && code <= 499:
			class = service.ErrUpstreamRejected
		case code >= 200 && code <= 299:
			class = service.ErrInvalidOutput
		}
	} else {
		switch {
		case strings.HasPrefix(header, "aborterror"):
			return err
		case strings.HasPrefix(header, "parsing error:"), strings.HasPrefix(header, "finish reason error:"):
			class = service.ErrInvalidOutput
		default:
			if exception := bamlException.FindStringSubmatch(message); len(exception) == 2 {
				switch exception[1] {
				case "throttlingexception", "modelnotreadyexception":
					class = service.ErrThrottled
				case "modeltimeoutexception", "internalserverexception", "serviceunavailableexception":
					class = service.ErrUpstreamUnavailable
				case "validationexception", "accessdeniedexception", "resourcenotfoundexception", "servicequotaexceededexception", "modelerrorexception":
					class = service.ErrUpstreamRejected
				}
			}
			if class == nil && (strings.Contains(header, "dispatchfailure") || strings.Contains(header, "timeout") || strings.Contains(header, "timed out") || strings.Contains(header, "connection refused") || strings.Contains(header, "connection reset")) {
				class = service.ErrUpstreamUnavailable
			}
		}
	}
	if class == nil {
		return err
	}
	return fmt.Errorf("%w: %w", class, err)
}
