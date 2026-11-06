package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/valyala/fasthttp"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

type streamDoneKey struct{}

// StreamCompletion reports whether a stream ended after a failed client write.
// The result is delivered once, after the stream releases its request context.
func StreamCompletion(c fiber.Ctx) <-chan bool {
	done, _ := c.Locals(streamDoneKey{}).(<-chan bool)
	return done
}

type planEvent struct {
	name    string
	payload any
}

// handlePlanStream reports completed topics followed by a validated plan or error.
// @Summary Stream adaptive study plan progress
// @Description Emit topic events with topic, zero-based index of its first occurrence in failed_topics, and items_generated; plan carries the same validated response as the JSON route; error carries error, details and the equivalent HTTP status. Failed topic calls report zero items. Flush each event and a : keepalive comment every 2 seconds; cancel work after a failed client write. REQUEST_TIMEOUT bounds generation. Invalid input returns JSON before streaming starts. Optional mastered_topics uses the same validation and prerequisite exclusions as the JSON route.
// @ID streamAdaptivePlan
// @Tags Adaptive Plan
// @Accept json
// @Produce json,text/event-stream
// @Security InternalToken
// @Param body body domain.AdaptivePlanRequest true "Student profile and failed concepts"
// @Success 200 {string} string "Server-sent events: topic, then plan or error"
// @Failure 400 {object} domain.ErrorResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 408 {object} domain.ErrorResponse
// @Failure 413 {object} domain.ErrorResponse
// @Failure 415 {object} domain.ErrorResponse
// @Failure 422 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Failure 431 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Failure 503 {object} domain.ErrorResponse "Shared storage unavailable or server shutdown"
// @Header all {string} X-Request-ID "Resolved request correlation identifier"
// @Header 200,400,413,415,422 {integer} X-RateLimit-Limit "Maximum requests per rate-limit window"
// @Header 200,400,413,415,422 {integer} X-RateLimit-Remaining "Requests remaining in the rate-limit window"
// @Header 200,400,413,415,422 {integer} X-RateLimit-Reset "Seconds until the rate-limit window resets"
// @Header 429,503 {integer} Retry-After "Seconds to wait before retrying"
// @x-events {"topic":{"description":"Completed topic call","payload":{"topic":"Recursion","index":0,"items_generated":1}},"plan":{"schema":{"$ref":"#/definitions/domain.PlanResponse"}},"error":{"description":"Usual error envelope plus equivalent HTTP status","payload":{"error":"bad_gateway","details":"upstream reasoning failed","status":502}}}
// @Router /api/v1/adaptive-plan/stream [post]
func (h *Handler) handlePlanStream(c fiber.Ctx) error {
	req, accepted, err := readPlanRequest(c)
	if !accepted {
		return err
	}
	if _, err := h.reasoning.ValidateRequest(req); err != nil {
		return h.writePipelineError(c, c.Context(), err)
	}
	parent := c.Context()
	requestID := strings.Clone(requestid.FromContext(c))
	ctx, cancel := context.WithTimeout(service.WithRequestID(parent, requestID), h.timeout)
	done := make(chan bool, 1)
	c.Locals(streamDoneKey{}, (<-chan bool)(done))
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("X-Accel-Buffering", "no")
	c.Response().ImmediateHeaderFlush = true
	stream := fasthttp.NewStreamReader(func(w *bufio.Writer) {
		disconnected := false
		defer func() { cancel(); done <- disconnected; close(done) }()
		if err := writeKeepalive(w); err != nil {
			disconnected = true
			return
		}
		events := make(chan planEvent, len(req.FailedTopics)+1)
		go func() {
			plan, err := h.reasoning.GeneratePlan(ctx, req, service.WithProgress(func(progress domain.TopicProgress) {
				select {
				case events <- planEvent{name: "topic", payload: progress}:
				case <-ctx.Done():
				}
			}))
			event := planEvent{name: "plan", payload: plan}
			if err != nil {
				failure := h.classifyPipelineError(ctx, requestID, err, parent.Err() != nil)
				event = planEvent{name: "error", payload: domain.PlanStreamError{ErrorResponse: failure.envelope, Status: failure.status}}
			}
			events <- event
		}()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case event := <-events:
				if err := writePlanEvent(w, event); err != nil {
					disconnected = true
					return
				}
				if event.name != "topic" {
					return
				}
			case <-ticker.C:
				if err := writeKeepalive(w); err != nil {
					disconnected = true
					return
				}
			}
		}
	})
	return c.SendStream(&planStreamReader{ReadCloser: stream, cancel: cancel})
}

type planStreamReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

// Close cancels generation when the response stops consuming the stream.
func (r *planStreamReader) Close() error {
	r.cancel()
	return r.ReadCloser.Close()
}

func writeKeepalive(w *bufio.Writer) error {
	if _, err := w.WriteString(": keepalive\n\n"); err != nil {
		return err
	}
	return w.Flush()
}

func writePlanEvent(w *bufio.Writer, event planEvent) error {
	payload, err := json.Marshal(event.payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, payload); err != nil {
		return err
	}
	return w.Flush()
}
