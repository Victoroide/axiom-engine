package telemetry

import "github.com/prometheus/client_golang/prometheus"

var langsmithDroppedSpans = newLangSmithDropCounter()

func newLangSmithDropCounter() *prometheus.CounterVec {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "axiom_langsmith_dropped_spans_total", Help: "LangSmith spans discarded by bounded buffering."}, []string{"reason"})
	for _, reason := range []string{"buffer_full", "group_full", "missing_parent", "queue_full", "shutdown"} {
		counter.WithLabelValues(reason)
	}
	return counter
}

// LangSmithDroppedSpansCollector exposes process-wide counts with fixed reasons.
func LangSmithDroppedSpansCollector() prometheus.Collector { return langsmithDroppedSpans }

func countDroppedSpans(reason string, count int) {
	if count > 0 {
		langsmithDroppedSpans.WithLabelValues(reason).Add(float64(count))
	}
}
