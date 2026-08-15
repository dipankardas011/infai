package memoryfit

import "errors"

// ErrCannotEstimate identifies models or hardware for which a trustworthy
// single-device memory estimate cannot be produced.
var ErrCannotEstimate = errors.New("memory estimate cannot be determined")
