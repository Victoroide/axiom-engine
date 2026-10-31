// @title AxiomEngine API
// @version 1.0
// @description Graph-guided adaptive study plans using Amazon Bedrock.
// @BasePath /
// @securityDefinitions.apikey InternalToken
// @in header
// @name X-Internal-Token

// Package main starts the reasoning service or runs its health probe.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/llm"
	"github.com/Victoroide/axiom-engine/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("server.failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if err := config.ValidateAWSRetry(os.LookupEnv); err != nil {
		return err
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if len(args) > 0 {
		if len(args) == 1 && args[0] == "healthcheck" {
			return server.Healthcheck(context.Background(), cfg.Port)
		}
		return fmt.Errorf("usage: axiom-server [healthcheck]")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)
	g, err := graph.Load(cfg.GraphFile)
	if err != nil {
		return err
	}
	app := server.New(cfg, server.Dependencies{Graph: g, Planner: llm.New(), Logger: logger})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case sig := <-signals:
			// Restore signal defaults before announcing shutdown or closing the listener.
			signal.Stop(signals)
			// Re-deliver a second signal queued before defaults were restored.
			select {
			case again := <-signals:
				if p, err := os.FindProcess(os.Getpid()); err == nil {
					_ = p.Signal(again)
				}
			default:
			}
			logger.Info("server.shutdown_started", "signal", sig.String(), "timeout", cfg.ShutdownTimeout.String())
			cancel()
		case <-ctx.Done():
		}
	}()
	return server.Run(ctx, app, ":"+cfg.Port, cfg.ShutdownTimeout)
}
