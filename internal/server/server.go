// Package server constructs the service HTTP application.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/gofiber/contrib/v3/swaggo"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/helmet"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/google/uuid"

	"github.com/Victoroide/axiom-engine/docs"
	"github.com/Victoroide/axiom-engine/internal/api"
	"github.com/Victoroide/axiom-engine/internal/config"
	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
	"github.com/Victoroide/axiom-engine/internal/service"
)

// Dependencies supplies the immutable graph, external planner and logger.
type Dependencies struct {
	Graph   *graph.InMemoryGraph
	Planner service.Planner
	Logger  *slog.Logger
}

// New constructs an application without opening a listener.
func New(cfg config.Config, deps Dependencies) *fiber.App {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	errorHandler := newErrorHandler(logger)
	app := newApp(cfg, errorHandler)
	app.State().Set(drainStateKey, &drainState{delay: cfg.ShutdownDrainDelay})
	useMiddleware(app, cfg, logger, errorHandler)
	registerPublicRoutes(app)
	registerAPI(app, cfg, deps, logger)
	return app
}

func newApp(cfg config.Config, errorHandler fiber.ErrorHandler) *fiber.App {
	return fiber.New(fiber.Config{
		AppName:            "axiom-engine",
		BodyLimit:          64 * 1024,
		ReadTimeout:        cfg.ReadTimeout,
		WriteTimeout:       cfg.WriteTimeout,
		IdleTimeout:        cfg.IdleTimeout,
		JSONEncoder:        json.Marshal,
		JSONDecoder:        json.Unmarshal,
		ErrorHandler:       errorHandler,
		TrustProxy:         true,
		TrustProxyConfig:   fiber.TrustProxyConfig{Proxies: cfg.TrustedProxies},
		ProxyHeader:        fiber.HeaderXForwardedFor,
		EnableIPValidation: true,
	})
}

func useMiddleware(app *fiber.App, cfg config.Config, logger *slog.Logger, errorHandler fiber.ErrorHandler) {
	app.Use(func(c fiber.Ctx) error {
		if base, ok := fiber.GetState[context.Context](app.State(), requestContextKey); ok {
			c.SetContext(base)
		}
		return c.Next()
	})
	app.Use(requestid.New())
	app.Use(func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		if err != nil {
			if handleErr := errorHandler(c, err); handleErr != nil {
				return handleErr
			}
		}
		logger.InfoContext(c.Context(), "http.access", "request_id", requestid.FromContext(c), "method", c.Method(), "path", c.Path(), "status", c.Response().StatusCode(), "duration_ms", float64(time.Since(start).Microseconds())/1000, "ip", c.IP())
		return nil
	})
	app.Use(recover.New(recover.Config{EnableStackTrace: true, StackTraceHandler: func(c fiber.Ctx, e any) {
		logger.ErrorContext(c.Context(), "panic.recovered", "request_id", requestid.FromContext(c), "error", fmt.Sprint(e), "stack", string(debug.Stack()))
	}}))
	app.Use(helmet.New())
	if len(cfg.CORSAllowedOrigins) > 0 {
		app.Use(cors.New(cors.Config{AllowOrigins: cfg.CORSAllowedOrigins, AllowMethods: []string{"GET", "POST", "HEAD", "OPTIONS"}, AllowHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Internal-Token", "X-Request-ID"}, ExposeHeaders: []string{"X-Request-ID"}, MaxAge: 3600}))
	}
	app.Use("/api/v1", limiter.New(limiter.Config{Max: cfg.RateLimitMax, Expiration: cfg.RateLimitWindow, LimiterMiddleware: limiter.SlidingWindow{}, LimitReached: func(c fiber.Ctx) error {
		return c.Status(fiber.StatusTooManyRequests).JSON(domain.ErrorResponse{Error: "rate_limit_exceeded", Details: "too many requests"})
	}}))
}

func registerPublicRoutes(app *fiber.App) {
	app.Get("/health", healthHandler)
	app.Get("/livez", livenessHandler)
	app.Get("/readyz", readinessHandler)
	// ReadDoc mutates its receiver; render a private copy once before serving.
	spec := *docs.SwaggerInfo
	specJSON := spec.ReadDoc()
	app.Get("/swagger/doc.json", func(c fiber.Ctx) error { return c.Type("json").SendString(specJSON) })
	app.Get("/swagger/*", swaggo.New(swaggo.Config{Title: "AxiomEngine API", ValidatorUrl: "none"}))
}

func registerAPI(app *fiber.App, cfg config.Config, deps Dependencies, logger *slog.Logger) {
	if !cfg.AuthDisabled {
		app.Use("/api/v1", internalTokenMiddleware(cfg.InternalToken))
	} else {
		logger.Warn("auth.disabled")
	}
	reasoning := service.NewReasoningService(deps.Graph, deps.Planner, service.Options{LLMTimeout: cfg.LLMTimeout, MaxFailedTopics: cfg.MaxFailedTopics, MaxConcurrency: cfg.LLMMaxConcurrency, QueueTimeout: cfg.LLMQueueTimeout, Logger: logger})
	api.NewHandler(reasoning, deps.Graph, cfg.RequestTimeout, cfg.MaxFailedTopics, logger).RegisterRoutes(app)
}

// healthHandler provides the legacy liveness alias.
// @Summary Health check
// @ID getHealth
// @Description Alias of /livez, reporting process liveness without authorization.
// @Tags System
// @Produce json
// @Success 200 {object} domain.HealthResponse
// @Router /health [get]
func healthHandler(c fiber.Ctx) error { return livenessHandler(c) }

// livenessHandler reports process liveness.
// @Summary Liveness probe
// @ID getLivez
// @Description Return process liveness, including during shutdown draining, without authorization.
// @Tags System
// @Produce json
// @Success 200 {object} domain.HealthResponse
// @Router /livez [get]
func livenessHandler(c fiber.Ctx) error { return c.JSON(domain.HealthResponse{Status: "ok"}) }

// readinessHandler reports whether the process is accepting traffic.
// @Summary Readiness probe
// @ID getReadyz
// @Description Return local readiness without authorization or upstream calls; report draining during shutdown.
// @Tags System
// @Produce json
// @Success 200 {object} domain.HealthResponse
// @Failure 503 {object} domain.HealthResponse "Server draining"
// @Router /readyz [get]
func readinessHandler(c fiber.Ctx) error {
	state := fiber.MustGetState[*drainState](c.App().State(), drainStateKey)
	if state.draining.Load() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(domain.HealthResponse{Status: "draining"})
	}
	return c.JSON(domain.HealthResponse{Status: "ok"})
}

func newErrorHandler(logger *slog.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		status := fiber.StatusInternalServerError
		var fiberErr *fiber.Error
		if errors.As(err, &fiberErr) {
			status = fiberErr.Code
		}
		code := strings.ReplaceAll(strings.ToLower(http.StatusText(status)), " ", "_")
		message := http.StatusText(status)
		if status >= 500 {
			if status == 500 {
				code = "internal_error"
			}
			logger.ErrorContext(c.Context(), "request.failed", "request_id", requestid.FromContext(c), "error_class", code)
		}
		if status == 413 {
			code = "payload_too_large"
		}
		// Parser-level failures may precede request middleware.
		if c.GetRespHeader("X-Request-ID") == "" {
			c.Set("X-Request-ID", uuid.NewString())
		}
		return c.Status(status).JSON(domain.ErrorResponse{Error: code, Details: message})
	}
}

func internalTokenMiddleware(expected string) fiber.Handler {
	expectedDigest := sha256.Sum256([]byte(expected))
	return func(c fiber.Ctx) error {
		provided := c.Get("X-Internal-Token")
		if provided == "" {
			if scheme, token, ok := strings.Cut(c.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
				provided = token
			}
		}
		providedDigest := sha256.Sum256([]byte(provided))
		if provided == "" || subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) != 1 {
			return c.Status(fiber.StatusUnauthorized).JSON(domain.ErrorResponse{Error: "unauthorized", Details: "missing or invalid internal token"})
		}
		return c.Next()
	}
}
