package service

import (
	"context"

	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

// WithRequestID attaches HTTP correlation without coupling the pipeline to transport.
func WithRequestID(ctx context.Context, id string) context.Context {
	return telemetry.WithRequestID(ctx, id)
}

func requestIDFromContext(ctx context.Context) string {
	return telemetry.RequestID(ctx)
}
