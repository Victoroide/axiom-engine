package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Victoroide/axiom-engine/internal/api"
)

// Run listens on the configured bind address and serves until ctx is canceled.
// An address that cannot be bound fails here, before anything is served.
func (m *MCPHTTP) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", m.remote.BindAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return m.Serve(ctx, ln)
}

// Serve serves TLS 1.2 or newer on ln until ctx is canceled, then stops
// accepting, lets admitted requests finish for the shutdown timeout and cancels
// the rest. It closes ln.
func (m *MCPHTTP) Serve(ctx context.Context, ln net.Listener) error {
	defer func() { _ = ln.Close() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	base, cancelCause := context.WithCancelCause(context.WithoutCancel(ctx))
	cancel := func() {
		cancelCause(api.ErrServerDraining)
		m.lifecycle.Cancel()
	}
	defer cancel()
	srv := &http.Server{
		Handler:        m.Handler(),
		TLSConfig:      &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{m.remote.Certificate}},
		ReadTimeout:    m.cfg.ReadTimeout,
		WriteTimeout:   m.cfg.WriteTimeout,
		IdleTimeout:    m.cfg.IdleTimeout,
		MaxHeaderBytes: 16 * 1024,
		// Handshake failures name remote addresses. Keep them out of the default log.
		ErrorLog:    slog.NewLogLogger(m.logger.Handler(), slog.LevelDebug),
		BaseContext: func(net.Listener) context.Context { return api.WithPeerCancellation(base) },
	}
	stopped := make(chan error, 1)
	go func() { stopped <- srv.ServeTLS(ln, "", "") }()
	m.logger.Info("mcp.listening", "address", ln.Addr().String(), "resource", m.remote.PublicURL, "origins", len(m.remote.AllowedOrigins))
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
	}
	m.lifecycle.StopAccepting()
	cancelTimer := time.AfterFunc(m.cfg.ShutdownTimeout-time.Second, cancel)
	defer cancelTimer.Stop()
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.ShutdownTimeout)
	defer stop()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = srv.Close()
	}
	serveErr := <-stopped
	if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", serveErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	return nil
}
