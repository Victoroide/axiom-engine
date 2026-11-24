package llm

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtime "github.com/boundaryml/baml/engine/language_client_go/pkg"

	baml "github.com/Victoroide/axiom-engine/baml_client"
	"github.com/Victoroide/axiom-engine/baml_client/types"
	"github.com/Victoroide/axiom-engine/internal/config"
)

const nativeTraceControlTopic = "native-trace-control-topic"

func TestAdapterOmitsCredentialsAtNativeCall(t *testing.T) {
	keys := []string{"BOUNDARY_SECRET", "BOUNDARY_PROJECT_ID", "BOUNDARY_API_KEY", "BOUNDARY_Secret", "BOUNDARY_Project_ID", "BOUNDARY_Api_Key"}
	for _, key := range keys {
		t.Setenv(key, "dummy-native-trace-value")
	}
	called := false
	adapter := newBAML(config.DefaultLLM(), func(_ context.Context, _ []types.GraphTuple, _ string, _ []string, _ string, opts ...baml.CallOptionFunc) (types.AdaptiveStudyPlan, error) {
		called = true
		if len(opts) == 0 {
			t.Fatal("native call options are missing")
		}
		// Apply the supplied options in generated-call order. The generated
		// client keeps its option type private and offers no env accessor.
		arg := reflect.New(reflect.TypeOf(opts[0]).In(0).Elem())
		for _, opt := range opts {
			reflect.ValueOf(opt).Call([]reflect.Value{arg})
		}
		env := arg.Elem().FieldByName("env")
		if !env.IsValid() || env.Kind() != reflect.Map {
			t.Fatal("cannot inspect the generated client environment")
		}
		for _, key := range keys {
			value := env.MapIndex(reflect.ValueOf(key))
			if !value.IsValid() || value.String() != "" {
				t.Errorf("native call did not remove %s", key)
			}
		}
		return types.AdaptiveStudyPlan{}, nil
	})
	if _, err := adapter.GenerateTopic(t.Context(), topicRequest()); err != nil || !called {
		t.Fatalf("native call reached = %t, error = %v", called, err)
	}
}

func TestAdapterDisablesNativeTraceExport(t *testing.T) {
	observations := &nativeTraceObservations{control: make(chan struct{})}
	receiver := httptest.NewServer(observations.handler(t, nil))
	t.Cleanup(receiver.Close)
	f := newProviderFixture(t, "openai", "unknown")
	t.Setenv("BOUNDARY_SECRET", "dummy-native-trace-secret")
	t.Setenv("BOUNDARY_Project_ID", "dummy-native-trace-project")
	t.Setenv("BOUNDARY_BASE_URL", receiver.URL)
	t.Setenv("BOUNDARY_SESSIONS_ID", "native-trace-fixture")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := New(f.settings).GenerateTopic(ctx, topicRequest())
	if err == nil || !strings.Contains(err.Error(), providerText) {
		t.Fatalf("expected native error to contain the diagnostic, got %v", err)
	}

	// The same configuration can export a diagnostic when used directly.
	registry := runtime.NewClientRegistry()
	registry.AddLlmClient("Fixture", f.settings.Provider, clientOptions(f.settings))
	registry.SetPrimaryClient("Fixture")
	_, err = baml.GenerateTopicPlan(ctx, nil, nativeTraceControlTopic, nil, "visual", baml.WithClientRegistry(registry))
	if err == nil || !strings.Contains(err.Error(), providerText) || f.count() != 2 {
		t.Fatalf("control error = %v, attempts = %d", err, f.count())
	}

	if problem := observations.finish(ctx, receiver); problem != "" {
		t.Fatal(problem)
	}
}

type nativeTraceObservations struct {
	control   chan struct{}
	validPost sync.Once
	forbidden atomic.Bool
}

func (o *nativeTraceObservations) handler(t *testing.T, afterRead func(string)) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read native trace: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/log/v2" {
			t.Errorf("unexpected native trace request: %s %s", r.Method, r.URL.Path)
		}
		text := string(body)
		if afterRead != nil {
			afterRead(text)
		}
		switch {
		case !strings.Contains(text, nativeTraceControlTopic):
			o.forbidden.Store(true)
		case !strings.Contains(text, providerText):
			t.Error("native trace control did not export the provider diagnostic")
		default:
			o.validPost.Do(func() { close(o.control) })
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}
}

func (o *nativeTraceObservations) finish(ctx context.Context, receiver *httptest.Server) string {
	select {
	case <-o.control:
	case <-ctx.Done():
	}
	// Close joins handlers admitted before shutdown, including a diagnostic
	// whose handler is still processing its body when the control arrives.
	receiver.Close()
	if o.forbidden.Load() {
		return "adapter exported a native trace"
	}
	select {
	case <-o.control:
		return ""
	default:
		return "native trace control did not reach the receiver"
	}
}

func TestNativeTraceReceiverDelayedNotifications(t *testing.T) {
	t.Run("control buffered after driver wait ends", func(t *testing.T) {
		observations := &nativeTraceObservations{control: make(chan struct{})}
		receiver := httptest.NewServer(observations.handler(t, nil))
		t.Cleanup(receiver.Close)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := postNativeTrace(ctx, receiver, nativeTraceControlTopic+" "+providerText); err != nil {
			t.Fatal(err)
		}
		// Both the positive observation and the wait cancellation are ready.
		cancel()
		if problem := observations.finish(ctx, receiver); problem != "" {
			t.Fatal(problem)
		}
	})

	t.Run("diagnostic handler held after reading", func(t *testing.T) {
		observations := &nativeTraceObservations{control: make(chan struct{})}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		held, release := make(chan struct{}), make(chan struct{})
		receiver := httptest.NewUnstartedServer(observations.handler(t, func(body string) {
			if body == providerText {
				close(held)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		}))
		listener := &traceCloseListener{Listener: receiver.Listener, closed: make(chan struct{})}
		receiver.Listener = listener
		receiver.Start()
		t.Cleanup(receiver.Close)

		posted := make(chan error, 1)
		go func() { posted <- postNativeTrace(ctx, receiver, providerText) }()
		if _, ok := receiveProviderEvent(held, ctx.Done()); !ok {
			t.Fatal("receiver did not read the diagnostic")
		}
		if err := postNativeTrace(ctx, receiver, nativeTraceControlTopic+" "+providerText); err != nil {
			t.Fatal(err)
		}
		finished := make(chan string, 1)
		go func() { finished <- observations.finish(ctx, receiver) }()
		// The driver has seen the positive control and begun closing the
		// receiver while the diagnostic handler is still held.
		if _, ok := receiveProviderEvent(listener.closed, ctx.Done()); !ok {
			t.Fatal("driver did not start closing the receiver")
		}
		close(release)
		problem, ok := receiveProviderEvent(finished, ctx.Done())
		if !ok || problem != "adapter exported a native trace" {
			t.Fatalf("held diagnostic verdict = %q, finished = %t", problem, ok)
		}
		if err, ok := receiveProviderEvent(posted, ctx.Done()); !ok || err != nil {
			t.Fatalf("diagnostic post error = %v, finished = %t", err, ok)
		}
	})
}

type traceCloseListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *traceCloseListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

func postNativeTrace(ctx context.Context, receiver *httptest.Server, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, receiver.URL+"/log/v2", strings.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := receiver.Client().Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
