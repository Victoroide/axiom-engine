package metrics

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestConcurrentObservations(t *testing.T) {
	m := New()
	var calls sync.WaitGroup
	for range 20 {
		calls.Go(func() {
			for range 50 {
				m.StartCall()
				m.ObserveLLM("ok", time.Millisecond)
				m.ObserveQueueWait(time.Millisecond, false)
				m.FinishCall()
				if _, err := m.registry.Gather(); err != nil {
					t.Error(err)
				}
			}
		})
	}
	calls.Wait()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		switch family.GetName() {
		case "axiom_llm_calls_total":
			for _, metric := range family.GetMetric() {
				if metric.GetLabel()[0].GetValue() == "ok" && metric.GetCounter().GetValue() != 1000 {
					t.Fatalf("calls=%v", metric.GetCounter().GetValue())
				}
			}
		case "axiom_llm_inflight":
			if got := family.GetMetric()[0].GetGauge().GetValue(); got != 0 {
				t.Fatalf("inflight=%v", got)
			}
		}
	}
}

func TestLatencyBucketsCoverDeadlines(t *testing.T) {
	m := New()
	m.ObserveHTTP("/health", "GET", 200, 20*time.Second)
	m.ObserveLLM("deadline", 15*time.Second)
	m.ObserveQueueWait(300*time.Millisecond, false)
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		upper float64
	}{
		{"http_server_request_duration_seconds", 20},
		{"axiom_llm_duration_seconds", 16},
		{"axiom_llm_queue_wait_seconds", 0.5},
	} {
		found := false
		for _, family := range families {
			if family.GetName() != tc.name {
				continue
			}
			for _, metric := range family.GetMetric() {
				for _, bucket := range metric.GetHistogram().GetBucket() {
					if bucket.GetUpperBound() == tc.upper && bucket.GetCumulativeCount() == 1 {
						found = true
					}
				}
			}
		}
		if !found {
			t.Errorf("%s has no finite bucket at %g containing the observation", tc.name, tc.upper)
		}
	}
}

func TestRuntimeCollectors(t *testing.T) {
	m := New()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, family := range families {
		found[family.GetName()] = true
	}
	if !found["go_goroutines"] {
		t.Error("goroutine count is missing")
	}
	if (runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows") && !found["process_cpu_seconds_total"] {
		t.Error("process CPU usage is missing")
	}
}
