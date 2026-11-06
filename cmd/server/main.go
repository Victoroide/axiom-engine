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
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/llm"
	"github.com/Victoroide/axiom-engine/internal/server"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) (err error) {
	var level slog.LevelVar
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: &level}))
	slog.SetDefault(logger)
	defer func() {
		if err != nil {
			logger.Error("server.failed", "error", err)
		}
	}()
	if err := config.ValidateAWSRetry(os.LookupEnv); err != nil {
		return err
	}
	if err := config.ValidateBAMLLog(os.LookupEnv); err != nil {
		return err
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	level.Set(cfg.LogLevel)
	if len(args) > 0 {
		if len(args) == 1 && args[0] == "healthcheck" {
			return server.Healthcheck(context.Background(), cfg.Port)
		}
		return fmt.Errorf("usage: axiom-server [healthcheck]")
	}
	g, err := graph.Load(cfg.GraphFile)
	if err != nil {
		return err
	}
	logStartup(logger, cfg, g, os.Getenv, os.ReadFile)
	app, err := server.NewWithError(cfg, server.Dependencies{Graph: g, Planner: llm.New(), Logger: logger})
	if err != nil {
		return err
	}
	app.Hooks().OnListen(func(data fiber.ListenData) error {
		logger.Info("server.listening", "address", net.JoinHostPort(strings.Trim(data.Host, "[]"), data.Port))
		return nil
	})
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

func logStartup(logger *slog.Logger, cfg config.Config, g *graph.InMemoryGraph, getenv func(string) string, readFile func(string) ([]byte, error)) {
	auth := "token"
	if cfg.AuthDisabled {
		auth = "disabled"
	} else if cfg.InternalTokenPrevious != "" {
		auth = "token+previous"
	}
	source := "embedded"
	if cfg.GraphFile != "" {
		source = "file"
	}
	version := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/boundaryml/baml" {
				version = dep.Version
				break
			}
		}
	}
	regionSource := config.AWSRegionSource(getenv, readFile)
	logger.Info("server.started",
		"port", cfg.Port, "auth", auth,
		"swagger_enabled", cfg.SwaggerEnabled, "metrics_enabled", cfg.MetricsEnabled,
		"rate_limit_max", cfg.RateLimitMax, "rate_limit_window", cfg.RateLimitWindow.String(),
		"request_timeout", cfg.RequestTimeout.String(), "llm_timeout", cfg.LLMTimeout.String(),
		"llm_queue_timeout", cfg.LLMQueueTimeout.String(), "shutdown_timeout", cfg.ShutdownTimeout.String(),
		"shutdown_drain_delay", cfg.ShutdownDrainDelay.String(),
		"llm_max_concurrency", cfg.LLMMaxConcurrency, "max_failed_topics", cfg.MaxFailedTopics,
		"cors_origins_count", len(cfg.CORSAllowedOrigins), "trusted_proxies_count", len(cfg.TrustedProxies),
		"graph_source", source, "graph_tuples", len(g.All()), "baml_version", version,
		"aws_region_source", regionSource)
	if regionSource == "" {
		metadata := "enabled"
		if getenv("AWS_EC2_METADATA_DISABLED") == "true" {
			metadata = "disabled"
		}
		logger.Warn("bedrock.region.unresolved", "instance_metadata", metadata,
			"detail", "instance metadata may still supply a region")
	}
}
