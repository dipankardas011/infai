package errors

import "errors"

var (
	ErrSessionNotFound    = errors.New("session not found")
	ErrEngineShuttingDown = errors.New("engine is shutting down")
	// ErrInvalidInput marks a request the client can fix (empty message,
	// unsupported modality, bad image bytes) so the server can return 400.
	ErrInvalidInput   = errors.New("invalid input")
	ErrTooManyClients = errors.New("too many session observers")
)

var (
	ErrSessionClosed  = errors.New("session closed")
	ErrNoTurnToCancel = errors.New("no turn is in flight")
	ErrTurnCanceled   = errors.New("turn canceled by user")
	ErrNoProvider     = errors.New("engine: no provider configured")
	ErrApprovalDenied = errors.New("tool execution was denied by the user")
)
