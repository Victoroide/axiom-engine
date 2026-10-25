package service

import "context"

type requestIDKey struct{}

// WithRequestID attaches HTTP correlation without coupling the pipeline to transport.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
