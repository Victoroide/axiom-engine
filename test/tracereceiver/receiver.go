// Package tracereceiver provides local OTLP/HTTP trace receivers for tests.
package tracereceiver

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// FailureBody is written by a receiver that was told to fail, so tests can
// check that receiver response bodies never reach logs.
const FailureBody = "SYNTHETIC-RECEIVER-FAILURE-BODY"

// Metadata is the resource and instrumentation scope that carried a span.
type Metadata struct {
	Resource map[string]string
	Scope    string
	ScopeKV  map[string]string
}

// Receiver records the decoded spans and raw payloads sent to a local endpoint.
type Receiver struct {
	server *httptest.Server
	path   string
	apiKey string

	mu       sync.Mutex
	status   int
	stall    chan struct{}
	spans    []*tracepb.Span
	meta     []Metadata
	bodies   [][]byte
	headers  []http.Header
	started  chan struct{}
	recorded chan struct{}
}

// NewOTLP starts a generic OTLP receiver that accepts traces at /v1/traces.
func NewOTLP(t testing.TB) *Receiver { return start(t, "/v1/traces", "") }

// NewLangSmith starts a receiver shaped like LangSmith's OTLP endpoint. It
// accepts traces at /otel/v1/traces and rejects requests without the API key.
func NewLangSmith(t testing.TB, apiKey string) *Receiver {
	return start(t, "/otel/v1/traces", apiKey)
}

func start(t testing.TB, path, apiKey string) *Receiver {
	t.Helper()
	r := &Receiver{path: path, apiKey: apiKey, started: make(chan struct{}, 1), recorded: make(chan struct{}, 1)}
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(func() {
		r.Release()
		r.server.Close()
	})
	return r
}

// URL is the receiver's base URL without the traces path.
func (r *Receiver) URL() string { return r.server.URL }

// TracesURL is the full URL accepting traces.
func (r *Receiver) TracesURL() string { return r.server.URL + r.path }

func (r *Receiver) serve(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil || req.Method != http.MethodPost || req.URL.Path != r.path || (r.apiKey != "" && req.Header.Get("x-api-key") != r.apiKey) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	select {
	case r.started <- struct{}{}:
	default:
	}
	r.mu.Lock()
	stall, status := r.stall, r.status
	r.mu.Unlock()
	if stall != nil {
		select {
		case <-stall:
		case <-req.Context().Done():
			return
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, FailureBody)
		return
	}
	if req.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err == nil {
			body, err = io.ReadAll(zr)
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	var batch collector.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &batch); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.bodies = append(r.bodies, body)
	r.headers = append(r.headers, req.Header.Clone())
	for _, resource := range batch.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			r.spans = append(r.spans, scope.Spans...)
			r.meta = append(r.meta, Metadata{Resource: stringAttributes(resource.GetResource().GetAttributes()), Scope: scope.GetScope().GetName(), ScopeKV: stringAttributes(scope.GetScope().GetAttributes())})
		}
	}
	r.mu.Unlock()
	select {
	case r.recorded <- struct{}{}:
	default:
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
}

// Fail makes later exports fail with the status. Zero restores success.
func (r *Receiver) Fail(status int) {
	r.mu.Lock()
	r.status = status
	r.mu.Unlock()
}

// Stall holds later exports until Release or until the exporter gives up.
func (r *Receiver) Stall() {
	r.mu.Lock()
	if r.stall == nil {
		r.stall = make(chan struct{})
	}
	r.mu.Unlock()
}

// Release lets stalled exports complete.
func (r *Receiver) Release() {
	r.mu.Lock()
	if r.stall != nil {
		close(r.stall)
		r.stall = nil
	}
	r.mu.Unlock()
}

// Started receives a value when an export request has arrived at the receiver.
func (r *Receiver) Started() <-chan struct{} { return r.started }

// Recorded signals that the receiver has recorded another export payload.
func (r *Receiver) Recorded() <-chan struct{} { return r.recorded }

// Spans returns every span received so far.
func (r *Receiver) Spans() []*tracepb.Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*tracepb.Span(nil), r.spans...)
}

// Requests returns the number of accepted export requests.
func (r *Receiver) Requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// Headers returns the headers of the accepted export requests.
func (r *Receiver) Headers() []http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]http.Header(nil), r.headers...)
}

// Wire returns the decoded protobuf payloads received so far, joined together.
func (r *Receiver) Wire() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return bytes.Join(r.bodies, nil)
}

// Count returns how many times the text appears in the received payloads.
func (r *Receiver) Count(text string) int { return strings.Count(string(r.Wire()), text) }

// Attributes returns the span attributes keyed by span name. Names that repeat
// keep the union of their attribute keys.
func (r *Receiver) Attributes() map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, span := range r.Spans() {
		attrs := out[span.Name]
		if attrs == nil {
			attrs = map[string]string{}
			out[span.Name] = attrs
		}
		for _, a := range span.Attributes {
			attrs[a.Key] = a.Value.GetStringValue()
		}
	}
	return out
}

// Keys returns the sorted attribute keys of every span that has the name.
func (r *Receiver) Keys(name string) []string {
	seen := map[string]struct{}{}
	for _, span := range r.Spans() {
		if span.Name != name {
			continue
		}
		for _, a := range span.Attributes {
			seen[a.Key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringAttributes(attrs []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, a := range attrs {
		out[a.Key] = a.Value.GetStringValue()
	}
	return out
}

// Metadata returns the resource and instrumentation scope attributes of every
// span batch received so far.
func (r *Receiver) Metadata() []Metadata {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Metadata(nil), r.meta...)
}

// Values returns the raw values of the attribute on every span with the name,
// including structured values.
func (r *Receiver) Values(name, key string) []*commonpb.AnyValue {
	var out []*commonpb.AnyValue
	for _, span := range r.Spans() {
		if span.Name != name {
			continue
		}
		for _, a := range span.Attributes {
			if a.Key == key {
				out = append(out, a.Value)
			}
		}
	}
	return out
}
