package service

import "github.com/Victoroide/axiom-engine/internal/domain"

type planOptions struct {
	progress func(domain.TopicProgress)
}

// PlanOption configures one plan generation call.
type PlanOption func(*planOptions)

// WithProgress reports completed topic calls, including failed calls with zero
// items. The callback can be invoked concurrently and must return promptly.
func WithProgress(callback func(domain.TopicProgress)) PlanOption {
	return func(options *planOptions) { options.progress = callback }
}

// ValidateRequest resolves topics and validates input before generation starts.
func (s *ReasoningService) ValidateRequest(req domain.AdaptivePlanRequest) (domain.AdaptivePlanRequest, error) {
	return validateRequest(req, s.maxFailedTopics, s.maxMasteredTopics, s.graph)
}
