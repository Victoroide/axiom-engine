package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/api"
	"github.com/Victoroide/axiom-engine/internal/config"
)

const requestContextKey = "requestContext"
const drainStateKey = "drainState"
const listenerSettingsKey = "listenerSettings"

type listenerSettings struct {
	mode   config.HTTPListener
	logger *slog.Logger
}

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
	return serveListener(ctx, app, ln, shutdownTimeout)
}

func serveListener(ctx context.Context, app *fiber.App, ln net.Listener, shutdownTimeout time.Duration) error {
	defer func() { _ = ln.Close() }()
	base, cancelCause := context.WithCancelCause(context.WithoutCancel(ctx))
	cancel := func() { cancelCause(api.ErrServerDraining) }
	defer cancel()
	app.State().Set(requestContextKey, base)
	serve := func() error { return app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }
	shutdown := func(ctx context.Context) error {
		// Close the listener even if cancellation beats Fiber's startup.
		_ = ln.Close()
		return app.ShutdownWithContext(ctx)
	}
	if settings, ok := fiber.GetState[listenerSettings](app.State(), listenerSettingsKey); ok {
		mode := config.HTTPListenerFiber
		if settings.mode == config.HTTPListenerNetHTTP {
			mode = settings.mode
			srv := newHTTPServer(app, base)
			serve = func() error { return srv.Serve(httpReadListener{ln}) }
			shutdown = func(ctx context.Context) error {
				err := srv.Shutdown(ctx)
				if err != nil {
					_ = srv.Close()
				}
				// Run the application's shutdown hooks after the listener drains.
				return errors.Join(err, app.ShutdownWithContext(ctx))
			}
		}
		settings.logger.Info("server.listening", "address", ln.Addr().String(), "listener", mode)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- serve() }()
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
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer stop()
		shutdownErr := shutdown(shutdownCtx)
		serveErr := <-stopped
		if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) && !errors.Is(serveErr, http.ErrServerClosed) {
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
