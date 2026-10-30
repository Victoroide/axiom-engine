// Package metrics records bounded HTTP and upstream telemetry.
package metrics

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns the collectors for one application.
type Metrics struct {
	registry         *prometheus.Registry
	httpDuration     *prometheus.HistogramVec
	httpRequests     *prometheus.CounterVec
	llmCalls         *prometheus.CounterVec
	llmDuration      *prometheus.HistogramVec
	inflight         prometheus.Gauge
	queueWait        prometheus.Histogram
	queueTimeouts    prometheus.Counter
	itemsDropped     *prometheus.CounterVec
	resourcesDropped prometheus.Counter
	stateMu          sync.RWMutex
	breakerState     func() int
}

// New constructs an independent Prometheus registry.
func New() *Metrics {
	latencyBuckets := []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 12, 16, 20, 30}
	m := &Metrics{
		registry:         prometheus.NewRegistry(),
		httpDuration:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "http_server_request_duration_seconds", Buckets: latencyBuckets, Help: "HTTP request duration by route template."}, []string{"route"}),
		httpRequests:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "http_server_requests_total", Help: "HTTP requests by route template, bounded method and status."}, []string{"route", "method", "status"}),
		llmCalls:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "axiom_llm_calls_total", Help: "Dispatched upstream calls by pipeline outcome."}, []string{"outcome"}),
		llmDuration:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "axiom_llm_duration_seconds", Buckets: latencyBuckets, Help: "Upstream call duration until completion or enforced cancellation."}, []string{"outcome"}),
		inflight:         prometheus.NewGauge(prometheus.GaugeOpts{Name: "axiom_llm_inflight", Help: "Upstream calls still executing, including calls past their deadline."}),
		queueWait:        prometheus.NewHistogram(prometheus.HistogramOpts{Name: "axiom_llm_queue_wait_seconds", Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}, Help: "Time waiting for an upstream concurrency slot."}),
		queueTimeouts:    prometheus.NewCounter(prometheus.CounterOpts{Name: "axiom_llm_queue_timeouts_total", Help: "Concurrency queue waits that exhausted their admission timeout."}),
		itemsDropped:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "axiom_plan_items_dropped_total", Help: "Plan items removed during deduplication or graph validation."}, []string{"reason"}),
		resourcesDropped: prometheus.NewCounter(prometheus.CounterOpts{Name: "axiom_plan_resources_dropped_total", Help: "Resources removed by the output policy."}),
	}
	for _, outcome := range []string{"ok", "throttled", "unavailable", "rejected", "invalid_output", "deadline", "canceled", "error"} {
		m.llmCalls.WithLabelValues(outcome)
	}
	for _, reason := range []string{"duplicate", "validation"} {
		m.itemsDropped.WithLabelValues(reason)
	}
	state := prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "axiom_circuit_breaker_state", Help: "Circuit state: 0 closed, 1 half-open, 2 open."}, func() float64 {
		m.stateMu.RLock()
		source := m.breakerState
		m.stateMu.RUnlock()
		if source == nil {
			return 0
		}
		return float64(source())
	})
	m.registry.MustRegister(m.httpDuration, m.httpRequests, m.llmCalls, m.llmDuration, m.inflight, m.queueWait, m.queueTimeouts, m.itemsDropped, m.resourcesDropped, state, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves this application's Prometheus exposition.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// SetBreakerStateSource reads the current circuit state at scrape time.
func (m *Metrics) SetBreakerStateSource(source func() int) {
	if m != nil {
		m.stateMu.Lock()
		m.breakerState = source
		m.stateMu.Unlock()
	}
}

// ObserveHTTP records a request using bounded route and method labels.
func (m *Metrics) ObserveHTTP(route, method string, status int, elapsed time.Duration) {
	if m != nil {
		if status < 100 || status > 599 {
			status = http.StatusInternalServerError
		}
		m.httpDuration.WithLabelValues(route).Observe(elapsed.Seconds())
		m.httpRequests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	}
}

// ObserveLLM records one dispatched call, excluding queue and circuit rejections.
func (m *Metrics) ObserveLLM(outcome string, elapsed time.Duration) {
	if m != nil {
		switch outcome {
		case "ok", "throttled", "unavailable", "rejected", "invalid_output", "deadline", "canceled":
		default:
			outcome = "error"
		}
		m.llmCalls.WithLabelValues(outcome).Inc()
		m.llmDuration.WithLabelValues(outcome).Observe(elapsed.Seconds())
	}
}

// StartCall records a planner that is about to execute.
func (m *Metrics) StartCall() {
	if m != nil {
		m.inflight.Inc()
	}
}

// FinishCall records a planner that has actually returned.
func (m *Metrics) FinishCall() {
	if m != nil {
		m.inflight.Dec()
	}
}

// ObserveQueueWait records admission latency and queue saturation.
func (m *Metrics) ObserveQueueWait(elapsed time.Duration, timedOut bool) {
	if m != nil {
		m.queueWait.Observe(elapsed.Seconds())
		if timedOut {
			m.queueTimeouts.Inc()
		}
	}
}

// ObserveDroppedItems records item removals using fixed reason labels.
func (m *Metrics) ObserveDroppedItems(duplicates, validation int) {
	if m != nil {
		m.itemsDropped.WithLabelValues("duplicate").Add(float64(duplicates))
		m.itemsDropped.WithLabelValues("validation").Add(float64(validation))
	}
}

// ObserveDroppedResources records output-policy removals.
func (m *Metrics) ObserveDroppedResources(count int) {
	if m != nil {
		m.resourcesDropped.Add(float64(count))
	}
}
