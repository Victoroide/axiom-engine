package api

import (
	"context"
	"errors"
)

type peerCancellationKey struct{}

// ErrServerDraining identifies the cancellation sent to requests during shutdown.
var ErrServerDraining = errors.New("server shutting down")

// WithPeerCancellation marks a context whose children can be canceled by the
// HTTP transport when the peer leaves.
func WithPeerCancellation(ctx context.Context) context.Context {
	return context.WithValue(ctx, peerCancellationKey{}, true)
}

// ClientDisconnected reports cancellation of the incoming request by its peer.
func ClientDisconnected(ctx context.Context) bool {
	if ctx.Err() != context.Canceled {
		return false
	}
	enabled, _ := ctx.Value(peerCancellationKey{}).(bool)
	return enabled && errors.Is(context.Cause(ctx), context.Canceled)
}

func shuttingDown(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled) && !ClientDisconnected(ctx)
}
