package service

// TopicError identifies a failed topic without formatting the upstream payload.
type TopicError struct {
	Topic string
	cause error
}

// Error returns a bounded description independent of upstream error content.
func (e *TopicError) Error() string { return "topic planning failed" }

// Unwrap preserves classification and cancellation causes.
func (e *TopicError) Unwrap() error { return e.cause }
