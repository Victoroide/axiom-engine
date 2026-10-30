package service

import "fmt"

// ErrThrottled indicates an upstream capacity limit.
var ErrThrottled = fmt.Errorf("upstream throttled: %w", ErrUpstream)

// ErrUpstreamUnavailable indicates an upstream server, transport or timeout failure.
var ErrUpstreamUnavailable = fmt.Errorf("upstream unavailable: %w", ErrUpstream)

// ErrUpstreamRejected indicates an upstream rejection other than throttling.
var ErrUpstreamRejected = fmt.Errorf("upstream rejected request: %w", ErrUpstream)

// ErrInvalidOutput indicates an unusable upstream response.
var ErrInvalidOutput = fmt.Errorf("invalid upstream output: %w", ErrUpstream)

// TopicError identifies a failed topic without formatting the upstream payload.
type TopicError struct {
	Topic string
	cause error
}

// Error returns a bounded description independent of upstream error content.
func (e *TopicError) Error() string { return "topic planning failed" }

// Unwrap preserves classification and cancellation causes.
func (e *TopicError) Unwrap() error { return e.cause }
