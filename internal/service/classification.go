package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

// Failure is a client-safe error classification shared by service transports.
type Failure struct {
	Status     int
	Response   domain.ErrorResponse
	RetryAfter time.Duration
}

// ClassifyError maps pipeline failures without exposing upstream messages.
func ClassifyError(err error, shuttingDown bool) Failure {
	f := Failure{Status: http.StatusInternalServerError, Response: domain.ErrorResponse{Error: "internal_error", Details: "reasoning failed"}}
	var validation *ValidationError
	if errors.As(err, &validation) {
		f.Status = http.StatusBadRequest
		if len(validation.UnknownTopics) > 0 {
			f.Status = http.StatusUnprocessableEntity
		}
		f.Response = domain.ErrorResponse{Error: "validation_error", Details: validation.Details}
		return f
	}
	var unavailable *UnavailableError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		f.Status, f.Response.Error, f.Response.Details = http.StatusGatewayTimeout, "gateway_timeout", "reasoning exceeded deadline"
	case errors.Is(err, ErrThrottled):
		f.Status, f.Response.Error, f.Response.Details = http.StatusServiceUnavailable, "service_unavailable", "upstream throttled"
	case errors.Is(err, ErrCircuitOpen) || errors.As(err, &unavailable):
		f.Status, f.Response.Error, f.Response.Details = http.StatusServiceUnavailable, "service_unavailable", "reasoning temporarily unavailable"
	case shuttingDown:
		f.Status, f.Response.Error, f.Response.Details = http.StatusServiceUnavailable, "service_unavailable", "server shutting down"
	case errors.Is(err, context.Canceled):
		f.Status, f.Response.Error, f.Response.Details = http.StatusRequestTimeout, "request_timeout", "request canceled"
	case errors.Is(err, ErrInvalidOutput):
		f.Status, f.Response.Error, f.Response.Details = http.StatusBadGateway, "bad_gateway", "model returned no usable item"
	case errors.Is(err, ErrUpstream):
		f.Status, f.Response.Error, f.Response.Details = http.StatusBadGateway, "bad_gateway", "upstream reasoning failed"
	}
	if f.Status == http.StatusServiceUnavailable {
		f.RetryAfter = 15 * time.Second
		if errors.Is(err, ErrThrottled) {
			f.RetryAfter = 5 * time.Second
		}
		if errors.As(err, &unavailable) && unavailable.RetryAfter > 0 {
			f.RetryAfter = unavailable.RetryAfter
		}
		if shuttingDown {
			f.RetryAfter = time.Second
		}
	}
	return f
}
