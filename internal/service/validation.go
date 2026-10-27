package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Victoroide/axiom-engine/internal/domain"
	"github.com/Victoroide/axiom-engine/internal/graph"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// ValidationError identifies invalid input before any upstream call.
type ValidationError struct {
	Details       string
	UnknownTopics []string
}

// Error returns a client-safe description.
func (e *ValidationError) Error() string { return e.Details }

// ValidIdentifier reports whether an identifier is safe to include in a prompt.
func ValidIdentifier(id string) bool { return identifierPattern.MatchString(id) }

// ValidateTopics trims and deduplicates topics while preserving request order.
func ValidateTopics(field string, topics []string, maxTopics int, g *graph.InMemoryGraph) ([]string, error) {
	normalized := make([]string, 0, len(topics))
	seen := make(map[string]bool, len(topics))
	for _, topic := range topics {
		topic = strings.TrimSpace(topic)
		if topic != "" && !seen[topic] {
			normalized = append(normalized, topic)
			seen[topic] = true
		}
	}
	if len(normalized) == 0 {
		return nil, &ValidationError{Details: field + " must contain at least one topic"}
	}
	if len(normalized) > maxTopics {
		return nil, &ValidationError{Details: fmt.Sprintf("%s must contain at most %d unique topics", field, maxTopics)}
	}
	unknown := make([]string, 0)
	for _, topic := range normalized {
		if !g.NodeExists(topic) {
			unknown = append(unknown, topic)
		}
	}
	if len(unknown) > 0 {
		return nil, &ValidationError{Details: field + " contains unknown topics: " + strings.Join(unknown, ", "), UnknownTopics: unknown}
	}
	return normalized, nil
}

func validateRequest(req domain.AdaptivePlanRequest, maxTopics int, g *graph.InMemoryGraph) (domain.AdaptivePlanRequest, error) {
	switch req.VARKProfile {
	case "visual", "auditory", "read_write", "kinesthetic":
	default:
		return req, &ValidationError{Details: "vark_profile must be visual, auditory, read_write or kinesthetic"}
	}
	if req.StudentID != "" && !ValidIdentifier(req.StudentID) {
		return req, &ValidationError{Details: "student_id must match ^[A-Za-z0-9._:-]{1,128}$"}
	}
	if req.CourseID != "" && !ValidIdentifier(req.CourseID) {
		return req, &ValidationError{Details: "course_id must match ^[A-Za-z0-9._:-]{1,128}$"}
	}
	topics, err := ValidateTopics("failed_topics", req.FailedTopics, maxTopics, g)
	req.FailedTopics = topics
	return req, err
}
