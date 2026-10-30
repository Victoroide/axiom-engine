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
	"net/netip"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/gofiber/contrib/v3/swaggo"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
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
	"github.com/Victoroide/axiom-engine/internal/metrics"
	"github.com/Victoroide/axiom-engine/internal/service"
)

var clientRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

var previousTokenWarnings = &previousTokenLogGate{}

type previousTokenLogGate struct {
	mu   sync.Mutex
	last time.Time
}

func (g *previousTokenLogGate) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.last.IsZero() && now.Sub(g.last) < time.Minute {
		return false
	}
	g.last = now
	return true
}

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
	var recorder *metrics.Metrics
	if cfg.MetricsEnabled {
		recorder = metrics.New()
	}
	errorHandler := newErrorHandler(logger)
	app := newApp(cfg, errorHandler)
	app.State().Set(drainStateKey, &drainState{delay: cfg.ShutdownDrainDelay})
	useMiddleware(app, cfg, logger, errorHandler, recorder)
	registerPublicRoutes(app)
	if recorder != nil {
		app.Get("/metrics", adaptor.HTTPHandler(recorder.Handler()))
	}
	registerAPI(app, cfg, deps, logger, recorder)
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

func useMiddleware(app *fiber.App, cfg config.Config, logger *slog.Logger, errorHandler fiber.ErrorHandler, recorder *metrics.Metrics) {
	app.Use(func(c fiber.Ctx) error {
		if base, ok := fiber.GetState[context.Context](app.State(), requestContextKey); ok {
			c.SetContext(base)
		}
		return c.Next()
	})
	app.Use(func(c fiber.Ctx) error {
		if !clientRequestIDPattern.MatchString(c.Get("X-Request-ID")) {
			c.Request().Header.Del("X-Request-ID")
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
		if recorder != nil {
			route, method := "unmatched", "unmatched"
			if endpoint := c.Endpoint(); endpoint != nil {
				route, method = endpoint.Path, endpoint.Method
			}
			recorder.ObserveHTTP(route, method, c.Response().StatusCode(), time.Since(start))
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

func registerAPI(app *fiber.App, cfg config.Config, deps Dependencies, logger *slog.Logger, recorder *metrics.Metrics) {
	if !cfg.AuthDisabled {
		app.Use("/api/v1", internalTokenMiddleware(tokenDigests(cfg.InternalToken, cfg.InternalTokenPrevious), logger, recorder))
	} else {
		logger.Warn("auth.disabled")
	}
	app.Use("/api/v1", limiter.New(limiter.Config{Max: cfg.RateLimitMax, Expiration: cfg.RateLimitWindow, LimiterMiddleware: limiter.SlidingWindow{}, KeyGenerator: rateLimitKey, LimitReached: func(c fiber.Ctx) error {
		return c.Status(fiber.StatusTooManyRequests).JSON(domain.ErrorResponse{Error: "rate_limit_exceeded", Details: "too many requests"})
	}}))
	reasoning := service.NewReasoningService(deps.Graph, deps.Planner, service.Options{LLMTimeout: cfg.LLMTimeout, MaxFailedTopics: cfg.MaxFailedTopics, MaxConcurrency: cfg.LLMMaxConcurrency, QueueTimeout: cfg.LLMQueueTimeout, Logger: logger, Metrics: recorder})
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

func rateLimitKey(c fiber.Ctx) string {
	ip := c.IP()
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	address = address.Unmap()
	if address.Is6() {
		return netip.PrefixFrom(address, 64).Masked().String()
	}
	return address.String()
}

func tokenDigests(tokens ...string) [][sha256.Size]byte {
	digests := make([][sha256.Size]byte, 0, len(tokens))
	for _, token := range tokens {
		if token != "" {
			digests = append(digests, sha256.Sum256([]byte(token)))
		}
	}
	return digests
}

func internalTokenMiddleware(expected [][sha256.Size]byte, logger *slog.Logger, recorder *metrics.Metrics) fiber.Handler {
	return func(c fiber.Ctx) error {
		provided := c.Get("X-Internal-Token")
		if provided == "" {
			if scheme, token, ok := strings.Cut(c.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
				provided = token
			}
		}
		providedDigest := sha256.Sum256([]byte(provided))
		matched := 0
		previousMatched := 0
		for i, digest := range expected {
			match := subtle.ConstantTimeCompare(providedDigest[:], digest[:])
			matched |= match
			if i == 1 {
				previousMatched = match
			}
		}
		if provided == "" || matched != 1 {
			return c.Status(fiber.StatusUnauthorized).JSON(domain.ErrorResponse{Error: "unauthorized", Details: "missing or invalid internal token"})
		}
		if previousMatched == 1 {
			recorder.ObservePreviousToken()
			if previousTokenWarnings.allow(time.Now()) {
				logger.WarnContext(c.Context(), "auth.previous_token_used", "request_id", requestid.FromContext(c))
			}
		}
		return c.Next()
	}
}
