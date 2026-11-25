package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/llm"
	"github.com/Victoroide/axiom-engine/internal/server"
	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

// runMCPHTTP serves the protected remote MCP endpoint. Every setting is
// checked before the listener opens, and stopping this command withdraws the
// endpoint. The ordinary HTTP server and the stdio command never start it.
func runMCPHTTP(args []string, stdout io.Writer) (err error) {
	var level slog.LevelVar
	logger := slog.New(telemetry.LogHandler(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: &level})))
	slog.SetDefault(logger)
	defer func() {
		if err != nil {
			logger.Error("server.failed", "error", err)
		}
	}()
	if len(args) != 0 {
		return fmt.Errorf("usage: axiom-server mcp-http")
	}
	if err := config.ValidateBAMLLog(os.LookupEnv); err != nil {
		return err
	}
	cfg, remote, err := config.LoadMCPHTTP(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.LLM.Provider == "aws-bedrock" {
		if err := config.ValidateAWSRetry(os.LookupEnv); err != nil {
			return err
		}
	}
	level.Set(cfg.LogLevel)
	traces, err := prepareTracing(cfg, logger)
	if err != nil {
		return err
	}
	var reload *graphReload
	defer func() {
		if shutdownErr := finishServing(reload, traces); err == nil {
			err = shutdownErr
		}
	}()
	curriculum, err := loadServingCurriculum(cfg)
	if err != nil {
		return err
	}
	reload = startGraphReload(cfg, curriculum, logger)
	app, err := server.NewMCPHTTP(cfg, remote, server.Dependencies{Curriculum: curriculum, Planner: llm.New(cfg.LLM), Logger: logger}, buildVersion())
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case sig := <-signals:
			// Restore signal defaults before announcing shutdown so that a
			// second signal stops the process.
			signal.Stop(signals)
			select {
			case again := <-signals:
				if p, err := os.FindProcess(os.Getpid()); err == nil {
					_ = p.Signal(again)
				}
			default:
			}
			traces.setDeadline(time.Now().Add(cfg.ShutdownTimeout))
			reload.stop()
			logger.Info("server.shutdown_started", "signal", sig.String(), "timeout", cfg.ShutdownTimeout.String())
			cancel()
		case <-ctx.Done():
		}
	}()
	return app.Run(ctx)
}
