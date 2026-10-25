// @title           AxiomEngine API
// @version         1.0
// @description     GraphRAG reasoning microservice. Generates adaptive study plans
// @description     from student failed concepts using Amazon Nova Micro via BAML.
// @contact.name    AxiomLMS Engineering
// @host            localhost:8080
// @BasePath        /
// @schemes         http https

// Package main is the entrypoint for the axiom-reasoning-svc binary. It
// initializes structured JSON logging, constructs the dependency graph
// (knowledge graph, reasoning service, HTTP handler), configures the Fiber
// application with enterprise middleware (helmet, rate limiter, panic
// recovery), and manages graceful shutdown on SIGINT/SIGTERM.
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

	"github.com/Victoroide/axiom-reasoning-svc/internal/api"
	"github.com/Victoroide/axiom-reasoning-svc/internal/domain"
	"github.com/Victoroide/axiom-reasoning-svc/internal/graph"
	"github.com/Victoroide/axiom-reasoning-svc/internal/llm"
	"github.com/Victoroide/axiom-reasoning-svc/internal/service"

	_ "github.com/Victoroide/axiom-reasoning-svc/docs"

	"github.com/goccy/go-json"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/helmet"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	fiberSwagger "github.com/swaggo/fiber-swagger"
)

func main() {
	// ── Structured JSON logging ──────────────────────────────────────
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// ── Dependency wiring ────────────────────────────────────────────
	g := graph.NewInMemoryGraph()
	slog.Info("graph.loaded", "tuples", len(g.All()))

	reasoningSvc := service.NewReasoningService(g, llm.New(), service.Options{LLMTimeout: 15 * time.Second, MaxFailedTopics: 10})
	handler := api.NewHandler(reasoningSvc, g)

	// ── Fiber app (goccy/go-json for fast serialization) ─────────────
	app := fiber.New(fiber.Config{
		AppName:      "axiom-reasoning-svc",
		JSONEncoder:  json.Marshal,
		JSONDecoder:  json.Unmarshal,
		ErrorHandler: customErrorHandler,
	})

	// ── Enterprise Middlewares ────────────────────────────────────────

	// CORS — allowed origins from env, defaults to "*" for local dev
	allowedOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	if allowedOrigins == "" {
		allowedOrigins = "*"
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization",
		AllowCredentials: false,
		MaxAge:           3600,
	}))

	// Security headers (XSS, content-type sniffing, etc.)
	app.Use(helmet.New())

	// Rate limiter: 50 requests per minute per IP
	app.Use(limiter.New(limiter.Config{
		Max:               50,
		Expiration:        1 * time.Minute,
		LimiterMiddleware: limiter.SlidingWindow{},
		LimitReached: func(c *fiber.Ctx) error {
			slog.Warn("rate_limit.exceeded", "ip", c.IP())
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":   "rate_limit_exceeded",
				"details": "Too many requests. Max 50 per minute.",
			})
		},
	}))

	// Request logger
	app.Use(logger.New(logger.Config{
		Format:     "${time} ${status} ${method} ${path} ${latency}\n",
		TimeFormat: "2006-01-02T15:04:05.000Z07:00",
	}))

	// Panic recovery with slog stack trace
	app.Use(recover.New(recover.Config{
		EnableStackTrace: true,
		StackTraceHandler: func(c *fiber.Ctx, e interface{}) {
			slog.Error("panic.recovered",
				"error", fmt.Sprintf("%v", e),
				"stack", string(debug.Stack()),
				"path", c.Path(),
				"method", c.Method(),
			)
		},
	}))

	// ── Routes ───────────────────────────────────────────────────────

	// /health stays unauthenticated so Docker/K8s liveness probes work.
	app.Get("/health", healthHandler)
	app.Get("/swagger/*", fiberSwagger.WrapHandler)

	// Optional shared-secret enforcement for /api/v1/* endpoints. When
	// AXIOM_INTERNAL_TOKEN is set in the environment, every request to the
	// reasoning API must carry the same value in the X-Internal-Token header
	// (or HTTP basic-style "Authorization: Bearer <token>"). This blocks the
	// frontend from talking to Go directly and forces traffic through the
	// Django proxy, which is the trust boundary that holds JWT auth.
	internalToken := os.Getenv("AXIOM_INTERNAL_TOKEN")
	if internalToken != "" {
		slog.Info("auth.internal_token.enabled")
		app.Use("/api/v1", internalTokenMiddleware(internalToken))
	} else {
		slog.Warn("auth.internal_token.disabled",
			"hint", "set AXIOM_INTERNAL_TOKEN to require shared-secret on /api/v1/*",
		)
	}

	handler.RegisterRoutes(app)

	// ── Graceful shutdown ────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("server.starting", "port", port)
		if err := app.Listen(":" + port); err != nil {
			slog.Error("server.listen_error", "error", err.Error())
		}
	}()

	<-ctx.Done()
	slog.Info("server.shutdown_initiated")

	shutdownTimeout := 10 * time.Second
	if err := app.ShutdownWithTimeout(shutdownTimeout); err != nil {
		slog.Error("server.shutdown_error", "error", err.Error())
		os.Exit(1)
	}
	slog.Info("server.shutdown_complete")
}

// healthHandler godoc
// @Summary      Health check
// @Description  Returns 200 if the service is running. Used by Railway and Docker health checks.
// @Tags         System
// @Produce      json
// @Success      200  {object}  domain.HealthResponse
// @Router       /health [get]
func healthHandler(c *fiber.Ctx) error {
	return c.JSON(domain.HealthResponse{Status: "ok"})
}

// customErrorHandler is the Fiber-level catch-all error handler. It maps
// *fiber.Error instances to their native status code and wraps all other
// errors in a 500 Internal Server Error JSON envelope conforming to the
// domain.ErrorResponse schema.
func customErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}
	return c.Status(code).JSON(fiber.Map{
		"error":   "internal_error",
		"details": err.Error(),
	})
}

// internalTokenMiddleware returns a Fiber middleware that compares the
// request's X-Internal-Token header (or "Authorization: Bearer <token>")
// against the configured shared secret. Mismatches are logged and answered
// with 401 Unauthorized; the request never reaches the route handler.
func internalTokenMiddleware(expected string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		provided := c.Get("X-Internal-Token")
		if provided == "" {
			authz := c.Get("Authorization")
			const prefix = "Bearer "
			if len(authz) > len(prefix) && authz[:len(prefix)] == prefix {
				provided = authz[len(prefix):]
			}
		}
		if provided != expected {
			slog.Warn("auth.internal_token.rejected",
				"ip", c.IP(),
				"path", c.Path(),
				"has_header", provided != "",
			)
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error":   "unauthorized",
				"details": "missing or invalid internal token",
			})
		}
		return c.Next()
	}
}
