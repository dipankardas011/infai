package errors

import "errors"

var (
	ErrSessionNotFound    = errors.New("session not found")
	ErrEngineShuttingDown = errors.New("engine is shutting down")
	// ErrInvalidInput marks a request the client can fix (empty message,
	// unsupported modality, bad image bytes) so the server can return 400.
	ErrInvalidInput = errors.New("invalid input")
)

var (
	ErrSessionClosed  = errors.New("session closed")
	ErrNoProvider     = errors.New("engine: no provider configured")
	ErrApprovalDenied = errors.New("tool execution was denied by the user")
)
