package llm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/service"
	"github.com/Victoroide/axiom-engine/test/fakebedrock"
)

func bedrockEndpoint(t *testing.T, mode string) *fakebedrock.Server {
	t.Helper()
	s := fakebedrock.New(mode)
	t.Cleanup(s.Close)
	t.Setenv("AWS_ENDPOINT_URL", s.URL)
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", s.URL)
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "false")
	configFile := filepath.Join(t.TempDir(), "aws-config")
	if err := os.WriteFile(configFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configFile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", configFile)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_DEFAULT_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "dummy")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "dummy")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_MAX_ATTEMPTS", "3")
	t.Setenv("AWS_RETRY_MODE", "standard")
	return s
}

func topicRequest() service.TopicRequest {
	return service.TopicRequest{
		GraphContext: []domain.GraphTuple{{Subject: "Recursion", Relation: "depends_on", Object: "Functions"}},
		Topic:        "Recursion", PrerequisiteOrder: []string{"Functions"}, VARKProfile: "visual",
		StudentID: "student-42", CourseID: "cs101",
	}
}

func TestAdapterReturnsPlanFromConverse(t *testing.T) {
	s := bedrockEndpoint(t, "ok")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, err := New().GenerateTopic(ctx, topicRequest())
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.PlanItem{{Topic: "Recursion", Priority: 1, PrerequisiteChain: []string{"Functions"}, Explanation: "Practice recursion.", Resources: []domain.Resource{{Title: "Guide", URL: "https://example.com/recursion", ResourceType: "video"}}}}
	if !reflect.DeepEqual(items, want) || s.Count() != 1 || len(s.Bodies()) != 1 {
		t.Fatalf("items = %+v, requests = %d", items, s.Count())
	}
}

func TestAdapterIsolatesAWSConfiguration(t *testing.T) {
	other := fakebedrock.New("denied")
	t.Cleanup(other.Close)
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", other.URL)
	t.Setenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "true")
	for _, key := range []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		path := filepath.Join(t.TempDir(), key)
		if err := os.WriteFile(path, []byte("[default]\nregion=us-west-2\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	s := bedrockEndpoint(t, "ok")
	if os.Getenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME") != s.URL || os.Getenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS") != "false" {
		t.Fatal("external endpoint settings remain active")
	}
	for _, key := range []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		data, err := os.ReadFile(os.Getenv(key)) //nolint:gosec // bedrockEndpoint replaces both paths with an empty file in t.TempDir.
		if err != nil || len(data) != 0 {
			t.Fatalf("%s is not an empty local file: %v", key, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := New().GenerateTopic(ctx, topicRequest()); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 1 || other.Count() != 0 {
		t.Fatalf("local requests = %d, external requests = %d", s.Count(), other.Count())
	}
}

func TestAdapterAbortsAtDeadline(t *testing.T) {
	s := bedrockEndpoint(t, "hang")
	const budget = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	_, err := New().GenerateTopic(ctx, topicRequest())
	if err == nil || ctx.Err() == nil || time.Since(start) > budget+250*time.Millisecond {
		t.Fatalf("error = %v, context = %v, elapsed = %s", err, ctx.Err(), time.Since(start))
	}
	select {
	case <-s.Disconnected():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("upstream connection remained open")
	}
	if s.Count() != 1 {
		t.Fatalf("requests = %d, want 1", s.Count())
	}
}

func TestAdapterDoesNotRetryParseFailures(t *testing.T) {
	s := bedrockEndpoint(t, "garbage")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := New().GenerateTopic(ctx, topicRequest()); err == nil {
		t.Fatal("expected a parsing error")
	}
	if s.Count() != 1 {
		t.Fatalf("requests = %d, want 1", s.Count())
	}
}

func TestAdapterReturnsEmptyItems(t *testing.T) {
	s := bedrockEndpoint(t, "empty")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, err := New().GenerateTopic(ctx, topicRequest())
	if err != nil || len(items) != 0 || s.Count() != 1 {
		t.Fatalf("items = %+v, error = %v, requests = %d", items, err, s.Count())
	}
}

func TestAdapterClassifiesFailures(t *testing.T) {
	for _, tt := range []struct {
		mode, fragment string
		want           error
	}{
		{"throttle", "status code: RateLimited (429)", service.ErrThrottled},
		{"denied", "status code: NotSupported (403)", service.ErrUpstreamRejected},
		{"validation", "status code: InvalidAuthentication (401)", service.ErrUpstreamRejected},
		{"not-found", "status code: Unspecified error code: 404", service.ErrUpstreamRejected},
		{"timeout", "status code: Unspecified error code: 408", service.ErrUpstreamUnavailable},
		{"error", "status code: Unspecified error code: 424", service.ErrUpstreamRejected},
		{"internal", "status code: ServerError (500)", service.ErrUpstreamUnavailable},
		{"unavailable", "status code: ServiceUnavailable (503)", service.ErrUpstreamUnavailable},
		{"malformed", "status code: Unspecified error code: 200", service.ErrInvalidOutput},
		{"html", "status code: Unspecified error code: 200", service.ErrInvalidOutput},
		{"max-tokens", "Finish reason error:", service.ErrInvalidOutput},
		{"garbage", "Parsing error:", service.ErrInvalidOutput},
		{"hang", "", context.DeadlineExceeded},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			s := bedrockEndpoint(t, tt.mode)
			budget := 15 * time.Second
			if tt.mode == "hang" {
				budget = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			_, err := New().GenerateTopic(ctx, topicRequest())
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v (requests = %d)", err, tt.want, s.Count())
			}
			if tt.fragment != "" && !strings.Contains(err.Error(), tt.fragment) {
				t.Fatalf("missing runtime status %q: %v", tt.fragment, err)
			}
			if tt.mode != "hang" && !errors.Is(err, service.ErrUpstream) {
				t.Fatalf("error does not wrap ErrUpstream: %v", err)
			}
		})
	}
}

func TestAdapterClassifiesTransportFailure(t *testing.T) {
	s := bedrockEndpoint(t, "ok")
	s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := New().GenerateTopic(ctx, topicRequest())
	if !errors.Is(err, service.ErrUpstreamUnavailable) || !errors.Is(err, service.ErrUpstream) {
		t.Fatalf("error = %v, want upstream unavailable", err)
	}
}

func TestAdapterRequestFailuresKeepCircuitAvailable(t *testing.T) {
	for _, mode := range []string{"validation", "denied", "malformed", "html", "garbage", "max-tokens"} {
		t.Run(mode, func(t *testing.T) {
			failed := bedrockEndpoint(t, mode)
			cfg, err := config.Load(func(k string) string {
				if k == "AXIOM_AUTH_DISABLED" {
					return "true"
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			app := server.New(cfg, server.Dependencies{Graph: graph.NewInMemoryGraph(), Planner: New(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
			call := func() int {
				t.Helper()
				req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/adaptive-plan", strings.NewReader(`{"failed_topics":["Recursion"],"vark_profile":"visual"}`))
				req.Header.Set("Content-Type", "application/json")
				resp, err := app.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				if err := resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
				return resp.StatusCode
			}
			for i := range 5 {
				if status := call(); status != http.StatusBadGateway {
					t.Fatalf("request %d: status = %d, want 502", i+1, status)
				}
			}
			ok := bedrockEndpoint(t, "ok")
			if status := call(); status != http.StatusOK || ok.Count() != 1 || failed.Count() != 5 {
				t.Fatalf("status = %d, successful requests = %d, failed requests = %d", status, ok.Count(), failed.Count())
			}
		})
	}
}
