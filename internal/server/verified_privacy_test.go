package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/llm"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

func TestVerifiedIdentityPrivacy(t *testing.T) {
	const tenant, caller, learner = "private-tenant-marker", "private-subject-marker", "private-learner-marker"
	const suppliedTenant, suppliedCaller = "ignored-tenant-marker", "ignored-subject-marker"
	cfg, fixture := newVerifiedFixture(t, false)
	cfg.Tracing.Enabled, cfg.Tracing.Content.OTLP, cfg.MetricsEnabled = true, true, true
	token := fixture.token(t, fixture.signer, tenant, caller, "graph:read plan:generate")
	markers := []string{tenant, caller, learner, suppliedTenant, suppliedCaller, token}
	assertPrivate := func(surface, data string) {
		t.Helper()
		for i, marker := range markers {
			if strings.Contains(data, marker) {
				t.Errorf("marker %d appeared in %s", i+1, surface)
			}
		}
	}
	received := make(chan string, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
			r.Header.Get("Authorization") != "Bearer provider-fixture-key" {
			t.Error("unexpected upstream request")
			http.Error(w, "unexpected upstream request", http.StatusBadRequest)
			return
		}
		var request bytes.Buffer
		_ = r.Header.Write(&request)
		request.Write(body)
		received <- request.String()
		const completion = `{"items":[{"topic":"Recursion","priority":1,"prerequisite_chain":["Functions"],"explanation":"Practice recursion.","resources":[]}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"fixture","object":"chat.completion","model":"fixture-model","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":23,"total_tokens":40}}`, completion)
	}))
	t.Cleanup(provider.Close)
	cfg.LLM = config.LLM{Provider: "openai", Model: "fixture-model", BaseURL: provider.URL + "/v1", APIKey: "provider-fixture-key"}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = tp.Shutdown(t.Context()) })
	logs := make(chan []byte, 64)
	logger := slog.New(telemetry.LogHandler(slog.NewJSONHandler(streamLogWriter{records: logs}, nil)))
	app := New(cfg, Dependencies{Graph: graph.NewInMemoryGraph(), Planner: llm.New(cfg.LLM), Logger: logger})
	headers := map[string]string{"Authorization": "Bearer " + token, "X-Tenant-ID": suppliedTenant, "X-Subject": suppliedCaller}
	for _, path := range []string{wirePlan, wireStream} {
		input := strings.Replace(wireTopicBody, "student-42", learner, 1)
		resp, body := call(t, app, http.MethodPost, path, input, headers)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, learner) || !strings.Contains(body, "Practice recursion.") {
			t.Fatalf("plan path=%s status=%d did not return provider content and the submitted learner", path, resp.StatusCode)
		}
		captured := receiveWire(t, received, "provider request")
		if !strings.Contains(captured, "Recursion") {
			t.Error("missing prompt in the upstream request")
		}
		assertPrivate("upstream request", captured)
		for _, record := range requestLogRecords(t, logs, "http.access") {
			assertPrivate("plan logs", string(record))
		}
	}
	graphRoute := "/api/v1/tutor/student/:student_id/cognitive-graph"
	graphPath := strings.Replace(graphRoute, ":student_id", learner, 1) + "?topics=Recursion"
	resp, _ := call(t, app, http.MethodGet, graphPath, "", headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graph status=%d", resp.StatusCode)
	}
	for _, record := range requestLogRecords(t, logs, "http.access") {
		assertPrivate("graph logs", string(record))
		if !bytes.Contains(record, []byte(`"path":"`+graphRoute+`"`)) {
			t.Error("graph access log is missing its route template")
		}
	}
	spans := panicRequestSpans(t, exporter, "POST "+wireStream)
	trace, err := json.Marshal(spans)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(trace, []byte("chat fixture-model")) || !bytes.Contains(trace, []byte("GET "+graphRoute)) {
		t.Error("missing provider or graph spans")
	}
	assertPrivate("spans", string(trace))
	resp, metrics := call(t, app, http.MethodGet, "/metrics", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(metrics, `route="`+graphRoute+`",status="200"`) ||
		!strings.Contains(metrics, `route="`+wireStream+`",status="200"`) {
		t.Error("missing graph or stream metric labels")
	}
	assertPrivate("metrics", metrics)
	select {
	case <-received:
		t.Error("unexpected extra upstream request")
	default:
	}
}

func TestLearnerAccessLogPrivacy(t *testing.T) {
	const learner = "private-learner-path-marker"
	const graphRoute = "/api/v1/tutor/student/:student_id/cognitive-graph"
	graphPath := strings.Replace(graphRoute, ":student_id", learner, 1)
	for _, mode := range []string{"shared default", "disabled", "verified"} {
		t.Run(mode, func(t *testing.T) {
			cfg := defaultConfig(t)
			headers := make(map[string]string)
			switch mode {
			case "shared default":
				secret := uuid.NewString()
				var err error
				cfg, err = config.Load(func(name string) string {
					if name == "AXIOM_INTERNAL_TOKEN" {
						return secret
					}
					return ""
				})
				if err != nil {
					t.Fatal(err)
				}
				headers["X-Internal-Token"] = secret
			case "verified":
				var fixture verifiedFixture
				cfg, fixture = newVerifiedFixture(t, false)
				headers["Authorization"] = "Bearer " + fixture.token(t, fixture.signer, "tenant", "caller", "graph:read")
			}
			logs := streamLogWriter{records: make(chan []byte, 10)}
			app := newTestApp(t, cfg, nil, logs)
			for _, tc := range []struct {
				method, path, wantPath string
				status                 int
			}{
				{http.MethodGet, graphPath + "?topics=Recursion", graphRoute, http.StatusOK},
				{http.MethodHead, graphPath + "?topics=Recursion", graphRoute, http.StatusOK},
				{http.MethodGet, graphPath + "/missing", "unmatched", http.StatusNotFound},
				{"PROPFIND", graphPath, "unmatched", http.StatusNotImplemented},
			} {
				resp, _ := call(t, app, tc.method, tc.path, "", headers)
				if resp.StatusCode != tc.status {
					t.Errorf("%s status=%d, want %d", tc.method, resp.StatusCode, tc.status)
				}
				records := requestLogRecords(t, logs.records, "http.access")
				for _, record := range records {
					if bytes.Contains(record, []byte(learner)) {
						t.Error("learner marker appeared in the access log")
					}
				}
				if !bytes.Contains(records[len(records)-1], []byte(`"path":"`+tc.wantPath+`"`)) {
					t.Errorf("access log path should be %q", tc.wantPath)
				}
			}
		})
	}
}
