package service

import (
	"fmt"
	"regexp"
	"strconv"
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

// ValidIdentifier reports whether an identifier matches the request format.
func ValidIdentifier(id string) bool { return identifierPattern.MatchString(id) }

// QuoteValue quotes a value for diagnostics, marking truncation beyond 128 runes.
func QuoteValue(s string) string {
	runes := 0
	for i := range s {
		if runes == 128 {
			return strconv.Quote(s[:i]) + "…"
		}
		runes++
	}
	return strconv.Quote(s)
}

// ValidateTopics resolves and deduplicates topics while preserving request order.
func ValidateTopics(field string, topics []string, maxTopics int, g *graph.InMemoryGraph) ([]string, error) {
	normalized := make([]string, 0, len(topics))
	seen := make(map[string]bool, len(topics))
	for _, topic := range topics {
		topic = strings.TrimSpace(topic)
		key := graph.Canonical(topic)
		if key != "" && !seen[key] {
			if resolved, ok := g.Resolve(topic); ok {
				topic = resolved
			}
			normalized = append(normalized, topic)
			seen[key] = true
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
		shown := unknown[:min(len(unknown), 10)]
		quoted := make([]string, len(shown))
		for i, topic := range shown {
			quoted[i] = QuoteValue(topic)
		}
		details := field + " contains unknown topics: " + strings.Join(quoted, ", ")
		if len(unknown) > len(shown) {
			details += fmt.Sprintf(" (+%d more)", len(unknown)-len(shown))
		}
		return nil, &ValidationError{Details: details, UnknownTopics: unknown}
	}
	return normalized, nil
}

func validateRequest(req domain.AdaptivePlanRequest, maxTopics, maxMastered int, g *graph.InMemoryGraph) (domain.AdaptivePlanRequest, error) {
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
	if err != nil {
		return req, err
	}
	req.FailedTopics = topics
	req.MasteredTopics, err = ValidateMasteredTopics("mastered_topics", req.MasteredTopics, topics, maxMastered, g)
	return req, err
}

// ValidateMasteredTopics resolves optional history and rejects contradictory evidence.
func ValidateMasteredTopics(field string, topics, failed []string, maxTopics int, g *graph.InMemoryGraph) ([]string, error) {
	if len(topics) == 0 {
		return topics, nil
	}
	mastered, err := ValidateTopics(field, topics, maxTopics, g)
	if err != nil {
		return nil, err
	}
	for _, topic := range mastered {
		for _, other := range failed {
			if graph.Canonical(topic) == graph.Canonical(other) {
				return nil, &ValidationError{Details: "a topic must not be both failed and mastered: " + QuoteValue(topic)}
			}
		}
	}
	return mastered, nil
}
