package browser

import "errors"

var ErrNotConnected = errors.New("browser extension is not connected")

// ActionError is an error the extension reported for an action.
type ActionError struct {
	Code    string // e.g. no_tab, tab_closed, stale_ref, invalid_args, timeout, debugger, internal
	Message string
}

func (e *ActionError) Error() string {
	return "browser " + e.Code + ": " + e.Message
}
