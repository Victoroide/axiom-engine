package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTraceFlushPanicPrivacy(t *testing.T) {
	const marker = "private-trace-shutdown-value"
	var logs bytes.Buffer
	traces := traceLifecycle{
		shutdown: func(context.Context) error { panic(errors.New(marker)) },
		timeout:  time.Second,
		logger:   slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	defer func() {
		if recover() != nil {
			t.Error("panic escaped trace shutdown")
		}
	}()
	traces.flush()
	var record map[string]string
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != "tracing.flush_failed" || record["level"] != "ERROR" || record["error_class"] != "trace_export_failed" || len(record) != 4 || logs.Len() > 1024 || strings.Contains(logs.String(), marker) {
		t.Error("expected one bounded trace flush failure log")
	}
}

type panickingLogWriter struct{}

func (panickingLogWriter) Write([]byte) (int, error) {
	panic(errors.New("private-trace-diagnostics-value"))
}

func TestTraceFlushDiagnosticPanicDoesNotEscape(t *testing.T) {
	traces := traceLifecycle{
		shutdown: func(context.Context) error { return errors.New("flush failed") },
		timeout:  time.Second,
		logger:   slog.New(slog.NewJSONHandler(panickingLogWriter{}, nil)),
	}
	defer func() {
		if recover() != nil {
			t.Error("panic escaped trace shutdown diagnostics")
		}
	}()
	traces.flush()
}
