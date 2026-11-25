package service

import (
	"context"
	"strconv"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

// TestTopicSpanNamesTheTopicOnlyWhenContentIsAllowed checks the topic name is not built into a span without consent.
func TestTopicSpanNamesTheTopicOnlyWhenContentIsAllowed(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run("capture="+strconv.FormatBool(capture), func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(tp)
			t.Cleanup(func() { otel.SetTracerProvider(previous); _ = tp.Shutdown(context.WithoutCancel(t.Context())) })
			svc := newService(func(_ context.Context, r TopicRequest) ([]domain.PlanItem, error) {
				return []domain.PlanItem{{Topic: r.Topic, Explanation: "Practice"}}, nil
			})
			if _, err := svc.GeneratePlan(telemetry.WithSettings(t.Context(), capture, false), request("Recursion")); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, span := range recorder.Ended() {
				if span.Name() != "plan.topic" {
					continue
				}
				found = true
				attrs := map[string]string{}
				for _, a := range span.Attributes() {
					attrs[string(a.Key)] = a.Value.String()
				}
				if attrs["axiom.topic.index"] != "0" {
					t.Errorf("topic index=%q", attrs["axiom.topic.index"])
				}
				if got, ok := attrs["axiom.topic"]; ok != capture || (capture && got != "Recursion") {
					t.Errorf("axiom.topic=%q present=%v with capture=%v", got, ok, capture)
				}
			}
			if !found {
				t.Fatal("topic span missing")
			}
		})
	}
}
