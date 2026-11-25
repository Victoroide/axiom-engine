package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestMCPHTTPTracesContainNoIdentityOrTokens(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	fx := newMCPFixture(t)
	i := fx.start(t, mcpOptions{settings: map[string]string{
		"TRACING_ENABLED": "true", "TRACING_CONTENT_DESTINATIONS": "otlp",
	}})
	token := fx.issuer.token(t, mcpPublicURL, privateID, callerID, allScopes)
	for _, reply := range []mcpReply{
		toolCall(t, i, token, "generate_study_plan", planArgs),
		toolCall(t, i, token, "list_topics", `{}`),
		toolCall(t, i, token, "get_topic_path", topicArgs),
	} {
		if reply.Status != http.StatusOK {
			t.Fatalf("status = %d: %s", reply.Status, reply.Body)
		}
	}
	spans := exporter.GetSpans()
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name)
	}
	if !strings.Contains(strings.Join(names, "\n"), "tools/call generate_study_plan") || !strings.Contains(strings.Join(names, "\n"), "tools/call list_topics") {
		t.Fatalf("tool spans are missing: %v", names)
	}
	exported := fmt.Sprintf("%+v", spans)
	signature := token[strings.LastIndex(token, ".")+1:]
	for _, secret := range []string{privateID, callerID, token, signature, fx.issuer.issuer, "Bearer"} {
		if strings.Contains(exported, secret) {
			t.Errorf("an exported span contains %q", secret)
		}
	}
}
