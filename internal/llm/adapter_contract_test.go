package llm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
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
