package api

import (
	"net/url"

	"github.com/gofiber/fiber/v3"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/service"
)

// handleTopics lists curriculum concepts without generating a study plan.
// @Summary List curriculum topics
// @ID listTopics
// @Tags Curriculum
// @Produce json
// @Security InternalToken
// @Param q query string false "Canonical substring of a topic name"
// @Success 200 {object} domain.TopicsResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 408 {object} domain.ErrorResponse
// @Failure 413 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Failure 431 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Header all {string} X-Request-ID "Resolved request correlation identifier"
// @Header 200,413 {integer} X-RateLimit-Limit "Maximum requests per rate-limit window"
// @Header 200,413 {integer} X-RateLimit-Remaining "Requests remaining in the rate-limit window"
// @Header 200,413 {integer} X-RateLimit-Reset "Seconds until the rate-limit window resets"
// @Header 429 {integer} Retry-After "Seconds to wait before retrying"
// @Router /api/v1/topics [get]
func (h *Handler) handleTopics(c fiber.Ctx) error {
	return c.JSON(domain.TopicsResponse{Topics: h.graph.Topics(c.Query("q"))})
}

// handleTopicPath returns the prerequisite study order for one concept.
// @Summary Get a topic's prerequisite path
// @ID getTopicPath
// @Tags Curriculum
// @Produce json
// @Security InternalToken
// @Param topic path string true "Topic name with canonical matching"
// @Success 200 {object} domain.TopicPathResponse
// @Failure 401 {object} domain.ErrorResponse
// @Failure 408 {object} domain.ErrorResponse
// @Failure 413 {object} domain.ErrorResponse
// @Failure 422 {object} domain.ErrorResponse
// @Failure 429 {object} domain.ErrorResponse
// @Failure 431 {object} domain.ErrorResponse
// @Failure 500 {object} domain.ErrorResponse
// @Header all {string} X-Request-ID "Resolved request correlation identifier"
// @Header 200,413,422 {integer} X-RateLimit-Limit "Maximum requests per rate-limit window"
// @Header 200,413,422 {integer} X-RateLimit-Remaining "Requests remaining in the rate-limit window"
// @Header 200,413,422 {integer} X-RateLimit-Reset "Seconds until the rate-limit window resets"
// @Header 429 {integer} Retry-After "Seconds to wait before retrying"
// @Router /api/v1/topics/{topic}/path [get]
func (h *Handler) handleTopicPath(c fiber.Ctx) error {
	topic := c.Params("topic")
	if decoded, err := url.PathUnescape(topic); err == nil {
		topic = decoded
	}
	path, ok := h.graph.Path(topic)
	if !ok {
		return validationResponse(c, &service.ValidationError{Details: "unknown topic: " + service.QuoteValue(topic), UnknownTopics: []string{topic}})
	}
	return c.JSON(path)
}
