package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

func BenchmarkWirePlanLatency(b *testing.B) {
	benchmarkWirePlanLatency(b, 100, immediateWirePlanner)
}

func BenchmarkWirePlanLatency500ms(b *testing.B) {
	benchmarkWirePlanLatency(b, 2, func(ctx context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return wireItems(req), nil
		}
	})
}

func BenchmarkWirePlanProfile(b *testing.B) {
	const warmupPerClient = 100
	clients := runtime.GOMAXPROCS(0)
	for _, listener := range []config.HTTPListener{config.HTTPListenerFiber, config.HTTPListenerNetHTTP} {
		b.Run(string(listener), func(b *testing.B) {
			request := startWireBenchmark(b, listener, clients, warmupPerClient, immediateWirePlanner)
			if err := sampleWirePlan(request, clients, make([]time.Duration, clients*warmupPerClient)); err != nil {
				b.Fatal(err)
			}
			samples := make([]time.Duration, b.N)
			b.ResetTimer()
			if err := sampleWirePlan(request, clients, samples); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
		})
	}
}

func immediateWirePlanner(_ context.Context, req service.TopicRequest) ([]domain.PlanItem, error) {
	return wireItems(req), nil
}

func benchmarkWirePlanLatency(b *testing.B, warmupPerClient int, planner fakePlanner) {
	b.Helper()
	clients := runtime.GOMAXPROCS(0)
	fiber := startWireBenchmark(b, config.HTTPListenerFiber, clients, warmupPerClient, planner)
	netHTTP := startWireBenchmark(b, config.HTTPListenerNetHTTP, clients, warmupPerClient, planner)
	for _, request := range []func() error{fiber, netHTTP} {
		if err := sampleWirePlan(request, clients, make([]time.Duration, clients*warmupPerClient)); err != nil {
			b.Fatal(err)
		}
	}
	fiberLatency, netHTTPLatency := make([]time.Duration, b.N), make([]time.Duration, b.N)
	batches := min(100, max(1, b.N/(5*clients)))
	b.ResetTimer()
	for batch := range batches {
		start, end := batch*b.N/batches, (batch+1)*b.N/batches
		first, second := fiber, netHTTP
		firstSamples, secondSamples := fiberLatency[start:end], netHTTPLatency[start:end]
		if batch%2 != 0 {
			first, second = second, first
			firstSamples, secondSamples = secondSamples, firstSamples
		}
		if err := sampleWirePlan(first, clients, firstSamples); err != nil {
			b.Fatal(err)
		}
		if err := sampleWirePlan(second, clients, secondSamples); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	fiberP95, netHTTPP95 := wireP95(fiberLatency), wireP95(netHTTPLatency)
	b.ReportMetric(float64(fiberP95.Nanoseconds()), "fiber-p95-ns")
	b.ReportMetric(float64(netHTTPP95.Nanoseconds()), "net-http-p95-ns")
	b.ReportMetric((float64(netHTTPP95)/float64(fiberP95)-1)*100, "p95-change-%")
}

func sampleWirePlan(request func() error, clients int, samples []time.Duration) error {
	var group errgroup.Group
	for client := range clients {
		group.Go(func() error {
			for i := client; i < len(samples); i += clients {
				start := time.Now()
				if err := request(); err != nil {
					return err
				}
				samples[i] = time.Since(start)
			}
			return nil
		})
	}
	return group.Wait()
}

func wireP95(samples []time.Duration) time.Duration {
	slices.Sort(samples)
	return samples[len(samples)-1-len(samples)/20]
}

func startWireBenchmark(b *testing.B, listener config.HTTPListener, clients, warmupPerClient int, planner fakePlanner) func() error {
	b.Helper()
	cfg := defaultConfig(b)
	cfg.HTTPListener = listener
	cfg.RateLimitMax = b.N + clients*warmupPerClient + 1
	app := newTestApp(b, cfg, planner, nil)
	ln, err := (&net.ListenConfig{}).Listen(b.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	ctx, stop := context.WithCancel(b.Context())
	done := make(chan error, 1)
	go func() { done <- serveListener(ctx, app, ln, cfg.ShutdownTimeout) }()
	b.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			b.Error(err)
		}
	})
	transport := &http.Transport{MaxIdleConnsPerHost: clients, MaxConnsPerHost: clients}
	b.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: wireSocketWait}
	url := "http://" + ln.Addr().String() + wirePlan
	return func() error { return benchmarkWirePlan(b.Context(), client, url) }
}

func benchmarkWirePlan(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(wireTopicBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, resp.Body)
	if err := errors.Join(readErr, resp.Body.Close()); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("plan status %d", resp.StatusCode)
	}
	return nil
}
