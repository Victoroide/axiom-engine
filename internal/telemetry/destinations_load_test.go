package telemetry

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/test/tracereceiver"
)

var bothExporters = map[string]string{"TRACING_ENABLED": "true", "LANGSMITH_TRACING": "true"}

func withDestinations(list string) map[string]string {
	env := map[string]string{"TRACING_CONTENT_DESTINATIONS": list}
	for key, value := range bothExporters {
		env[key] = value
	}
	return env
}

func waitForSpans(t *testing.T, receiver *tracereceiver.Receiver, want int) {
	t.Helper()
	waitForExportState(t, func() bool { return len(receiver.Spans()) >= want })
	if got := len(receiver.Spans()); got != want {
		t.Fatalf("spans=%d want %d", got, want)
	}
}

func TestRemoteParentsKeepTheirMeaningWithContentViews(t *testing.T) {
	f := startDestinations(t, withDestinations("langsmith"))
	remote := callerSpanContext(t)
	emitPlanTrace(f.settingsContext(t.Context()), "-remote", remote)
	if err := f.stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, span := range f.langsmith.Spans() {
		ids[hex.EncodeToString(span.SpanId)] = true
	}
	roots := 0
	for _, span := range f.langsmith.Spans() {
		if len(span.ParentSpanId) == 0 {
			roots++
		} else if !ids[hex.EncodeToString(span.ParentSpanId)] {
			t.Errorf("LangSmith span %s has a parent outside the group", span.Name)
		}
	}
	if len(f.langsmith.Spans()) != 3 || roots != 1 {
		t.Fatalf("LangSmith spans=%d roots=%d", len(f.langsmith.Spans()), roots)
	}
	remoteID := remote.SpanID()
	for _, span := range f.otlp.Spans() {
		if span.Name == "POST /api/v1/adaptive-plan" && !bytes.Equal(span.ParentSpanId, remoteID[:]) {
			t.Errorf("OTLP root parent=%x want the remote caller", span.ParentSpanId)
		}
	}
	assertNoContentMarkers(t, "OTLP", f.otlp)
	if f.langsmith.Count(promptMarker+"-remote") != 1 {
		t.Fatal("LangSmith did not receive approved content for the re-rooted group")
	}
}

func TestGraphOnlyTracesReachOnlyGenericOTLPWithoutContent(t *testing.T) {
	f := startDestinations(t, withDestinations("langsmith,otlp"))
	ctx := f.settingsContext(t.Context())
	ctx, root := Start(ctx, "GET /api/v1/topics", attribute.String("http.route", "/api/v1/topics"))
	_, child := Start(ctx, "graph.topics")
	child.End()
	root.End()
	if err := f.stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if len(f.otlp.Spans()) != 2 || f.langsmith.Requests() != 0 {
		t.Fatalf("OTLP spans=%d LangSmith requests=%d", len(f.otlp.Spans()), f.langsmith.Requests())
	}
	assertNoContentMarkers(t, "OTLP", f.otlp)
}

func TestConcurrentExportKeepsContentSeparated(t *testing.T) {
	const traces = 48
	f := startDestinations(t, withDestinations("langsmith"))
	var wg sync.WaitGroup
	for i := range traces {
		wg.Go(func() {
			remote := trace.SpanContext{}
			if i%2 == 0 {
				remote = callerSpanContext(t)
			}
			emitPlanTrace(f.settingsContext(t.Context()), "-"+strconv.Itoa(i), remote)
		})
	}
	wg.Wait()
	if err := f.stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if len(f.otlp.Spans()) != traces*3 || len(f.langsmith.Spans()) != traces*3 {
		t.Fatalf("OTLP spans=%d LangSmith spans=%d", len(f.otlp.Spans()), len(f.langsmith.Spans()))
	}
	assertNoContentMarkers(t, "OTLP", f.otlp)
	for _, marker := range contentMarkers {
		if n := f.langsmith.Count(marker); n != traces {
			t.Errorf("LangSmith received %d copies of %s, want %d", n, marker, traces)
		}
	}
}

func TestShutdownDuringExportNeverLeaksContent(t *testing.T) {
	const traces = 5
	// Keep OTLP's partial batch queued until shutdown flushes it.
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "600000")
	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "512")
	f := startDestinations(t, withDestinations("langsmith"))
	f.langsmith.Stall()
	defer f.langsmith.Release()
	for i := range traces {
		emitPlanTrace(f.settingsContext(t.Context()), "-"+strconv.Itoa(i), trace.SpanContext{})
	}
	watchdog, cancel := context.WithTimeout(t.Context(), shutdownWatchdog)
	defer cancel()
	waitForReceiverStart(t, watchdog, f.langsmith, "LangSmith")
	done := startShutdown(f, watchdog)
	// OTLP's drain proves shutdown is underway while LangSmith is still in flight.
	waitForReceiverSpans(t, watchdog, f.otlp, "OTLP", traces*3)
	f.langsmith.Release()
	if err := waitForShutdown(t, watchdog, done); err != nil {
		t.Fatal(err)
	}
	assertNoContentMarkers(t, "OTLP", f.otlp)
	if got := len(f.langsmith.Spans()); got != traces*3 {
		t.Fatalf("LangSmith spans=%d, want %d", got, traces*3)
	}
}

func TestStalledLangSmithSaturatesWithoutBlockingRequestsOrLeakingContent(t *testing.T) {
	const traces = 200
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "20")
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("TMPDIR", directory)
	f := startDestinations(t, withDestinations("langsmith"))
	f.langsmith.Stall()
	before := dropCount(t, "queue_full")
	emitPlanTrace(f.settingsContext(t.Context()), "-0", trace.SpanContext{})
	select {
	case <-f.langsmith.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("LangSmith export never started")
	}
	start := time.Now()
	for i := 1; i < traces; i++ {
		emitPlanTrace(f.settingsContext(t.Context()), "-"+strconv.Itoa(i), trace.SpanContext{})
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("request path blocked for %s behind a stalled receiver", elapsed)
	}
	// One group is in flight and 128 are queued; the rest are counted and dropped.
	if got, want := dropCount(t, "queue_full")-before, float64((traces-129)*3); got != want {
		t.Fatalf("queue drops=%v want %v", got, want)
	}
	waitForSpans(t, f.otlp, traces*3)
	assertNoContentMarkers(t, "OTLP", f.otlp)
	f.langsmith.Release()
	waitForSpans(t, f.langsmith, 129*3)
	if err := f.stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if n := f.langsmith.Count(promptMarker); n != 129 {
		t.Fatalf("LangSmith received %d approved prompts, want 129", n)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("trace export wrote files: %v err=%v", entries, err)
	}
}

func TestFailingReceiversStayIsolatedAndPrivate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed string
	}{{"OTLP fails", "otlp"}, {"LangSmith fails", "langsmith"}} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			f := startDestinations(t, withDestinations("langsmith,otlp"))
			healthy, failing := f.langsmith, f.otlp
			if tc.failed == "langsmith" {
				healthy, failing = f.otlp, f.langsmith
			}
			failing.Fail(500)
			for i := range 10 {
				emitPlanTrace(f.settingsContext(t.Context()), "-"+strconv.Itoa(i), trace.SpanContext{})
			}
			err := f.stop(5 * time.Second)
			if len(healthy.Spans()) != 30 || len(failing.Spans()) != 0 {
				t.Fatalf("healthy spans=%d failing spans=%d", len(healthy.Spans()), len(failing.Spans()))
			}
			for _, text := range append([]string{tracereceiver.FailureBody}, contentMarkers...) {
				if bytes.Contains(logs.Bytes(), []byte(text)) || (err != nil && bytes.Contains([]byte(err.Error()), []byte(text))) {
					t.Errorf("diagnostics contain %s", text)
				}
			}
		})
	}
}

const shutdownWatchdog = 15 * time.Second

func startShutdown(f *destinationFixture, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- f.shutdown(ctx) }()
	return done
}

func waitForShutdown(t *testing.T, watchdog context.Context, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-watchdog.Done():
		t.Fatal("shutdown did not return")
		return nil
	}
}

func waitForReceiverStart(t *testing.T, watchdog context.Context, receiver *tracereceiver.Receiver, name string) {
	t.Helper()
	select {
	case <-receiver.Started():
	case <-watchdog.Done():
		t.Fatalf("%s export never started", name)
	}
}

func waitForReceiverSpans(t *testing.T, watchdog context.Context, receiver *tracereceiver.Receiver, name string, want int) {
	t.Helper()
	for {
		got := len(receiver.Spans())
		if got >= want {
			if got != want {
				t.Fatalf("%s spans=%d, want %d", name, got, want)
			}
			return
		}
		select {
		case <-receiver.Recorded():
		case <-watchdog.Done():
			t.Fatalf("%s recorded %d of %d spans", name, got, want)
		}
	}
}

func TestShutdownStaysBoundedWhenReceiversStall(t *testing.T) {
	const traces = 5
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "600000")
	t.Run("both stalled", func(t *testing.T) {
		// Fill one OTLP batch so both handlers are already stalled before the deadline starts.
		t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", strconv.Itoa(traces*3))
		f := startDestinations(t, withDestinations("langsmith,otlp"))
		f.otlp.Stall()
		f.langsmith.Stall()
		defer f.otlp.Release()
		defer f.langsmith.Release()
		for i := range traces {
			emitPlanTrace(f.settingsContext(t.Context()), "-"+strconv.Itoa(i), trace.SpanContext{})
		}
		watchdog, stopWatchdog := context.WithTimeout(t.Context(), shutdownWatchdog)
		defer stopWatchdog()
		waitForReceiverStart(t, watchdog, f.otlp, "OTLP")
		waitForReceiverStart(t, watchdog, f.langsmith, "LangSmith")
		ctx, cancel := context.WithTimeout(watchdog, 300*time.Millisecond)
		defer cancel()
		if err := waitForShutdown(t, watchdog, startShutdown(f, ctx)); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown=%v, want the deadline", err)
		}
	})
	for _, stalledName := range []string{"OTLP", "LangSmith"} {
		t.Run(stalledName+" stalled", func(t *testing.T) {
			t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "512")
			f := startDestinations(t, withDestinations("langsmith,otlp"))
			stalled, healthy, healthyName := f.otlp, f.langsmith, "LangSmith"
			if stalledName == "LangSmith" {
				stalled, healthy, healthyName = f.langsmith, f.otlp, "OTLP"
			}
			stalled.Stall()
			defer stalled.Release()
			for i := range traces {
				emitPlanTrace(f.settingsContext(t.Context()), "-"+strconv.Itoa(i), trace.SpanContext{})
			}
			watchdog, stopWatchdog := context.WithTimeout(t.Context(), shutdownWatchdog)
			defer stopWatchdog()
			ctx, cancel := context.WithCancel(watchdog)
			defer cancel()
			done := startShutdown(f, ctx)
			waitForReceiverStart(t, watchdog, stalled, stalledName)
			waitForReceiverSpans(t, watchdog, healthy, healthyName, traces*3)
			cancel()
			if err := waitForShutdown(t, watchdog, done); !errors.Is(err, context.Canceled) {
				t.Fatalf("shutdown=%v, want cancellation", err)
			}
		})
	}
}

func TestBoundsHoldWithContentViews(t *testing.T) {
	f := startDestinations(t, withDestinations("langsmith,otlp"))
	ctx := f.settingsContext(t.Context())
	groupBefore, bufferBefore := dropCount(t, "group_full"), dropCount(t, "buffer_full")
	tracer := f.provider.Tracer("bounds")
	groupCtx, root := tracer.Start(ctx, "request")
	for range langsmithMaxGroupSpans {
		_, child := tracer.Start(groupCtx, "chat", trace.WithAttributes(attribute.String("langsmith.span.kind", "llm"), attribute.String("gen_ai.prompt.0.content", promptMarker)))
		child.End()
	}
	root.End()
	if got := dropCount(t, "group_full") - groupBefore; got != langsmithMaxGroupSpans+1 {
		t.Fatalf("group drops=%v", got)
	}
	roots := make([]trace.Span, 0, langsmithMaxBufferedSpans+1)
	for range langsmithMaxBufferedSpans + 1 {
		_, open := tracer.Start(ctx, "open request")
		roots = append(roots, open)
	}
	if got := dropCount(t, "buffer_full") - bufferBefore; got != 1 {
		t.Fatalf("buffer drops=%v", got)
	}
	for _, open := range roots {
		open.End()
	}
	if err := f.stop(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if f.langsmith.Requests() != 0 {
		t.Fatal("an over-limit or model-free group reached LangSmith")
	}
}

func TestShutdownStopsLangSmithWhenOTLPUsesTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expired bool
		wantErr error
	}{{"OTLP holds the deadline", false, context.DeadlineExceeded}, {"deadline already passed", true, context.Canceled}} {
		t.Run(tc.name, func(t *testing.T) {
			f := startDestinations(t, withDestinations("langsmith,otlp"))
			ctx := f.settingsContext(t.Context())
			// OTLP is registered first and keeps its final export open. LangSmith has
			// delivered one finished group and holds an unfinished model call.
			f.otlp.Stall()
			emitPlanTrace(ctx, "-finished", trace.SpanContext{})
			waitForSpans(t, f.langsmith, 3)
			select {
			case <-f.langsmith.Started():
			default:
			}
			requestCtx, root := Start(ctx, "POST /api/v1/adaptive-plan")
			callCtx, call := Start(requestCtx, "chat planner")
			Annotate(callCtx, call, "llm", "chat planner")
			call.SetAttributes(attribute.String("gen_ai.prompt.0.content", promptMarker+"-unfinished"))
			requests, dropsBefore := f.langsmith.Requests(), dropCount(t, "shutdown")

			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 300*time.Millisecond)
			defer cancel()
			if tc.expired {
				var cancelNow context.CancelFunc
				shutdownCtx, cancelNow = context.WithCancel(context.WithoutCancel(t.Context()))
				cancelNow()
			}
			start := time.Now()
			err := f.shutdown(shutdownCtx)
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("shutdown took %s", elapsed)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("shutdown=%v want %v", err, tc.wantErr)
			}
			// LangSmith stopped even though OTLP used the deadline: its open group is discarded.
			if got := dropCount(t, "shutdown") - dropsBefore; got != 2 {
				t.Fatalf("LangSmith dropped %v open spans at shutdown, want 2", got)
			}
			if err := f.stop(time.Second); err != nil {
				t.Fatalf("second shutdown=%v", err)
			}
			call.End()
			root.End()
			select {
			case <-f.langsmith.Started():
				t.Fatal("LangSmith exported after shutdown")
			case <-time.After(250 * time.Millisecond):
			}
			if got := f.langsmith.Requests(); got != requests || f.langsmith.Count(promptMarker+"-unfinished") != 0 {
				t.Fatalf("LangSmith requests=%d want %d", got, requests)
			}
		})
	}
}
