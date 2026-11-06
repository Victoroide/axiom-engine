package mcpserver

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/service"
)

// Lifecycle tracks admitted tool calls and cancels them when draining expires.
type Lifecycle struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	stopping bool
	active   int
	drained  chan struct{}
}

// NewLifecycle creates a shared cancellation boundary for tool calls.
func NewLifecycle(ctx context.Context) *Lifecycle {
	ctx, cancel := context.WithCancel(ctx)
	return &Lifecycle{ctx: ctx, cancel: cancel, drained: make(chan struct{})}
}

// StopAccepting rejects subsequent calls and signals when admitted calls finish.
func (l *Lifecycle) StopAccepting() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.stopping {
		l.stopping = true
		if l.active == 0 {
			close(l.drained)
		}
	}
	return l.drained
}

// Cancel cancels every admitted call, including stdio request contexts.
func (l *Lifecycle) Cancel() { l.cancel() }

// Middleware admits tool calls and combines client and shutdown cancellation.
func (l *Lifecycle) Middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		l.mu.Lock()
		if l.stopping {
			l.mu.Unlock()
			failure := service.ClassifyError(context.Canceled, true).Response
			data, _ := json.Marshal(failure)
			return &mcp.CallToolResult{IsError: true, StructuredContent: failure, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
		}
		l.active++
		l.mu.Unlock()
		// The request context is canceled after the SDK writes its response.
		// Keep the call admitted until both the handler and response have finished.
		requestCtx := ctx
		defer func() { context.AfterFunc(requestCtx, l.finish) }()

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(l.ctx, cancel) //nolint:contextcheck // Combine shutdown cancellation with the inherited request context.
		defer stop()
		if l.ctx.Err() != nil {
			cancel()
		}
		return next(ctx, method, req)
	}
}

func (l *Lifecycle) finish() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active--
	if l.stopping && l.active == 0 {
		close(l.drained)
	}
}
