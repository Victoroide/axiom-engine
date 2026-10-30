package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/service"
)

func TestClassifyBAMLMessages(t *testing.T) {
	tests := []struct {
		name, message string
		want          error
	}{
		{"throttled", `LLM client "BedrockNovaMicro" failed with status code: RateLimited (429)`, service.ErrThrottled},
		{"denied", `LLM client "BedrockNovaMicro" failed with status code: NotSupported (403) Message: AccessDeniedException`, service.ErrUpstreamRejected},
		{"bad request", `failed with status code: InvalidAuthentication (401)`, service.ErrUpstreamRejected},
		{"server error", "failed with status code: ServerError (500)\nMessage: validationerror finish reason", service.ErrUpstreamUnavailable},
		{"unavailable", `failed with status code: ServiceUnavailable (503)`, service.ErrUpstreamUnavailable},
		{"not found", "failed with status code: Unspecified error code: 404\nMessage: timeout connection reset", service.ErrUpstreamRejected},
		{"request error", "failed with status code: Unspecified error code: 424\nMessage: finish reason validationerror", service.ErrUpstreamRejected},
		{"request timeout", `failed with status code: Unspecified error code: 408`, service.ErrUpstreamUnavailable},
		{"malformed success", `failed with status code: Unspecified error code: 200`, service.ErrInvalidOutput},
		{"bad response", `failed with status code: BadResponse 200`, service.ErrInvalidOutput},
		{"empty response", `failed with status code: BadResponse 204`, service.ErrInvalidOutput},
		{"bad request response", `failed with status code: BadResponse 404`, service.ErrUpstreamRejected},
		{"client error boundary", `failed with status code: Unspecified error code: 20`, nil},
		{"parse", "Parsing error: Failed to parse LLM response: Missing required field: student_id", service.ErrInvalidOutput},
		{"parse with status text", "Parsing error: Missing required field\nRaw Response: failed with status code: ServerError (500)", service.ErrInvalidOutput},
		{"finish reason", "Finish reason error: disallowed finish reason\nFinish Reason: max_tokens", service.ErrInvalidOutput},
		{"dispatch", "failed with status code: Unspecified error code: 2 Message: DispatchFailure", service.ErrUpstreamUnavailable},
		{"transport", "DispatchFailure(connection closed)", service.ErrUpstreamUnavailable},
		{"timeout", `LLM client "BedrockNovaMicro" timed out: request deadline elapsed`, service.ErrUpstreamUnavailable},
		{"exception", "upstream error\n    source: ThrottlingException(", service.ErrThrottled},
		{"raw keywords", "unrecognized upstream error\nRaw Response: timeout connection reset validationerror finish reason", nil},
		{"abort", "AbortError: 2 failed attempts", nil},
		{"unknown", "unrecognized upstream error", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := errors.New(tt.message)
			err := classify(original)
			if !errors.Is(err, original) {
				t.Fatalf("original error lost: %v", err)
			}
			if tt.want == nil {
				if errors.Unwrap(err) != nil || err.Error() != original.Error() {
					t.Fatalf("unclassified error changed: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.want) || !errors.Is(err, service.ErrUpstream) {
				t.Fatalf("error = %v, want %v wrapping ErrUpstream", err, tt.want)
			}
			for _, other := range []error{service.ErrThrottled, service.ErrUpstreamRejected, service.ErrUpstreamUnavailable, service.ErrInvalidOutput} {
				if !errors.Is(other, tt.want) && errors.Is(err, other) {
					t.Fatalf("error also matches %v", other)
				}
			}
		})
	}
	if classify(nil) != nil {
		t.Fatal("nil error changed")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if !errors.Is(classify(err), err) {
			t.Fatalf("context error changed: %v", err)
		}
	}
}
