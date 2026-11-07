package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/llm"
	"github.com/Victoroide/axiom-engine/internal/mcpserver"
)

func runMCP(args []string) (err error) {
	var level slog.LevelVar
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: &level}))
	slog.SetDefault(logger)
	defer func() {
		if err != nil {
			logger.Error("server.failed", "error", err)
		}
	}()
	if len(args) != 0 {
		return fmt.Errorf("usage: axiom-server mcp")
	}
	if err := config.ValidateBAMLLog(os.LookupEnv); err != nil {
		return err
	}
	cfg, err := config.LoadMCP(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.LLM.Provider == "aws-bedrock" {
		if err := config.ValidateAWSRetry(os.LookupEnv); err != nil {
			return err
		}
	}
	level.Set(cfg.LogLevel)
	g, err := graph.Load(cfg.GraphFile)
	if err != nil {
		return err
	}
	logStartup(logger, cfg, g, os.Getenv, os.ReadFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifecycle := mcpserver.NewLifecycle(ctx)
	defer lifecycle.Cancel()
	s := mcpserver.New(cfg, g, llm.New(cfg.LLM), logger, buildVersion())
	s.AddReceivingMiddleware(lifecycle.Middleware)
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	session, err := s.Connect(ctx, &mcp.StdioTransport{}, nil)
	if err != nil {
		return err
	}
	stopped := make(chan error, 1)
	go func() { stopped <- session.Wait() }()
	select {
	case err := <-stopped:
		return err
	case sig := <-signals:
		// Restore defaults before announcing shutdown; preserve a queued second signal.
		signal.Stop(signals)
		select {
		case again := <-signals:
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(again)
			}
		default:
		}
		deadline := time.Now().Add(cfg.ShutdownTimeout)
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		drained := lifecycle.StopAccepting()
		logger.Info("server.shutdown_started", "signal", sig.String(), "timeout", cfg.ShutdownTimeout.String())
		select {
		case <-drained:
			lifecycle.Cancel()
			closed := make(chan error, 1)
			go func() { closed <- session.Close() }()
			select {
			case err := <-closed:
				if time.Now().Before(deadline) {
					if err != nil {
						return err
					}
					logger.Info("server.shutdown_completed")
					return nil
				}
			case <-timer.C:
			}
		case <-timer.C:
		}
		lifecycle.Cancel()
		// Closing can wait on stdout; return so the entry point exits without
		// appending another protocol frame to a possibly incomplete response.
		return fmt.Errorf("shutdown: %w", context.DeadlineExceeded)
	}
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
