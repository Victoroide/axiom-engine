// Package idempotency retains successful responses and reserves request keys.
package idempotency

import "context"

// State describes the result of reserving a request key.
type State int

const (
	// Acquired means the caller owns a new reservation.
	Acquired State = iota
	// Replay means a successful response is available.
	Replay
	// Mismatch means the key belongs to a different request body.
	Mismatch
	// InProgress means the matching request has not completed.
	InProgress
)

// Response contains only the fields preserved for a replay.
type Response struct {
	Status      int
	Body        []byte
	ContentType string
	// Format is empty for records that store the original HTTP body.
	Format string `json:",omitempty"`
}

// Reservation identifies ownership and any stored response.
type Reservation struct {
	Token    string
	Response Response
}

// Store atomically reserves keys and publishes successful responses.
type Store interface {
	// Begin can return a token with an error when the reservation may have
	// been created. Abort can safely release that token if it still owns it.
	Begin(context.Context, string, string) (Reservation, State, error)
	Complete(context.Context, string, string, Response) error
	Abort(context.Context, string, string) error
}
