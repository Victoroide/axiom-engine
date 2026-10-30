package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v3"
)

const requestContextKey = "requestContext"
const drainStateKey = "drainState"

type drainState struct {
	delay    time.Duration
	draining atomic.Bool
}

// Run serves until cancellation or a listener failure, then drains connections.
func Run(ctx context.Context, app *fiber.App, address string, shutdownTimeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = ln.Close() }()
	base, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	app.State().Set(requestContextKey, base)
	stopped := make(chan error, 1)
	go func() { stopped <- app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		if state, ok := fiber.GetState[*drainState](app.State(), drainStateKey); ok {
			state.draining.Store(true)
			drainTimer := time.NewTimer(state.delay)
			defer drainTimer.Stop()
			select {
			case <-drainTimer.C:
			case err := <-stopped:
				return err
			}
		}
		cancelTimer := time.AfterFunc(shutdownTimeout-time.Second, cancel)
		defer cancelTimer.Stop()
		// Close the listener even if cancellation beats Fiber's startup.
		_ = ln.Close()
		shutdownErr := app.ShutdownWithTimeout(shutdownTimeout)
		serveErr := <-stopped
		if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) {
			return fmt.Errorf("serve: %w", serveErr)
		}
		if shutdownErr != nil && !errors.Is(shutdownErr, net.ErrClosed) {
			return fmt.Errorf("shutdown: %w", shutdownErr)
		}
		return nil
	}
}

// Healthcheck probes the local liveness endpoint with a bounded deadline.
func Healthcheck(ctx context.Context, port string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/livez", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: status %d", resp.StatusCode)
	}
	return nil
}
