//go:build integration

package contract_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

const (
	quotaLatencyRequests = 100
	quotaLatencyInterval = 10 * time.Millisecond
)

var quotaLatencyModes = [2]string{"IP only", "IP plus tenant"}

func TestTenantQuotaLatencyPhases(t *testing.T) {
	for firstMode, name := range quotaLatencyModes {
		t.Run(name+" first", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				type event struct {
					mode     int
					finished bool
					started  time.Time
				}
				events := make(chan event, 4*quotaLatencyRequests)
				runs := measureAt100RPS(t, firstMode, func(_ context.Context, mode int) error {
					events <- event{mode: mode, started: time.Now()}
					time.Sleep(25 * time.Millisecond)
					events <- event{mode: mode, finished: true}
					return nil
				})
				close(events)
				var started, finished [2]int
				var lastStart [2]time.Time
				for e := range events {
					if e.finished {
						finished[e.mode]++
						continue
					}
					started[e.mode]++
					if e.mode != firstMode && finished[firstMode] != quotaLatencyRequests {
						t.Fatalf("%s started after %d of %d %s responses completed",
							quotaLatencyModes[e.mode], finished[firstMode], quotaLatencyRequests, name)
					}
					if last := lastStart[e.mode]; !last.IsZero() && e.started.Sub(last) != quotaLatencyInterval {
						t.Errorf("%s started requests %v apart, want %v", quotaLatencyModes[e.mode], e.started.Sub(last), quotaLatencyInterval)
					}
					lastStart[e.mode] = e.started
				}
				for mode, run := range runs {
					if started[mode] != quotaLatencyRequests || len(run.latencies) != quotaLatencyRequests || run.requestsPerSecond != 100 {
						t.Errorf("%s: started=%d samples=%d rate=%.2f, want 100 each",
							quotaLatencyModes[mode], started[mode], len(run.latencies), run.requestsPerSecond)
					}
				}
			})
		})
	}
}

func BenchmarkTenantQuotaLatency(b *testing.B) {
	for b.Loop() {
		measureTenantQuotaLatency(b)
	}
}

func measureTenantQuotaLatency(t testing.TB) {
	t.Helper()
	f := newTenantFixture(t)
	without := f.serve(t, map[string]string{"AXIOM_TENANT_QUOTA_ENABLED": "false", "RATE_LIMIT_MAX": "100000"}, nil)
	with := f.serve(t, map[string]string{"AXIOM_TENANT_QUOTA_MAX": "100000", "RATE_LIMIT_MAX": "100000"}, nil)
	_ = f.tenantKey(t, "latency-tenant")
	token := f.token(t, "latency-tenant", "latency-caller", "graph:read")
	ip := f.ip(140)
	endpoints := [2]string{without, with}
	for _, endpoint := range endpoints {
		for range 10 {
			f.check(t, endpoint, http.MethodGet, tenantTopics, token, ip, "", "", http.StatusOK)
		}
	}
	request := func(ctx context.Context, mode int) error {
		res, err := f.request(ctx, endpoints[mode], http.MethodGet, tenantTopics, token, ip, "", "")
		if err == nil && res.StatusCode != http.StatusOK {
			return fmt.Errorf("status=%d, want 200: %s", res.StatusCode, res.Body)
		}
		return err
	}
	var samples [2][]time.Duration
	for i := range 3 {
		for mode, run := range measureAt100RPS(t, i%2, request) {
			samples[mode] = append(samples[mode], run.latencies...)
			t.Logf("repetition %d, %s: %d requests at %.2f requests/s, p95=%v",
				i+1, quotaLatencyModes[mode], len(run.latencies), run.requestsPerSecond, quotaP95(run.latencies))
		}
	}
	baseline, enabled := quotaP95(samples[0]), quotaP95(samples[1])
	increment := enabled - baseline
	t.Logf("pooled at 100 requests/s, 300 samples per mode: IP only p95=%v, IP plus tenant p95=%v, increment=%v", baseline, enabled, increment)
	if increment >= 5*time.Millisecond {
		t.Fatalf("incremental p95=%v, want under 5ms", increment)
	}
}

type quotaLatencyRun struct {
	latencies         []time.Duration
	requestsPerSecond float64
}

func measureAt100RPS(t testing.TB, firstMode int, request func(context.Context, int) error) [2]quotaLatencyRun {
	t.Helper()
	var runs [2]quotaLatencyRun
	for i := range runs {
		mode := (firstMode + i) % len(runs)
		runs[mode] = measureQuotaLatencyPhase(t, mode, request)
	}
	return runs
}

func measureQuotaLatencyPhase(t testing.TB, mode int, request func(context.Context, int) error) quotaLatencyRun {
	t.Helper()
	type sample struct {
		started time.Time
		elapsed time.Duration
		err     error
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	results := make(chan sample, quotaLatencyRequests)
	phaseStart := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for i := range quotaLatencyRequests {
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatalf("%s: could not schedule the latency workload", quotaLatencyModes[mode])
		}
		go func() {
			start := time.Now()
			err := request(ctx, mode)
			results <- sample{start, time.Since(start), err}
		}()
		// Keep each launch on the same schedule through brief scheduler delays.
		next := phaseStart.Add(time.Duration(i+1) * quotaLatencyInterval)
		timer.Reset(time.Until(next))
	}
	run := quotaLatencyRun{latencies: make([]time.Duration, 0, quotaLatencyRequests)}
	var first, last time.Time
	for range quotaLatencyRequests {
		result := <-results
		if result.err != nil {
			t.Fatalf("%s: %v", quotaLatencyModes[mode], result.err)
		}
		run.latencies = append(run.latencies, result.elapsed)
		if first.IsZero() || result.started.Before(first) {
			first = result.started
		}
		if result.started.After(last) {
			last = result.started
		}
	}
	run.requestsPerSecond = float64(quotaLatencyRequests-1) / last.Sub(first).Seconds()
	if run.requestsPerSecond < 95 || run.requestsPerSecond > 105 {
		t.Fatalf("%s: achieved %.2f requests/s, want 100 within 5%%", quotaLatencyModes[mode], run.requestsPerSecond)
	}
	return run
}

func quotaP95(samples []time.Duration) time.Duration {
	slices.Sort(samples)
	return samples[(95*len(samples)+99)/100-1]
}
