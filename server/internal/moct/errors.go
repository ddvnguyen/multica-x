package moct

import (
	"errors"
	"fmt"
)

// ErrUnavailable reports that moct-be could not be reached (dial failure,
// timeout, reset, or a truncated read). Callers on the pre-run path treat it
// as "start the run without memory and log a warning" — memory never blocks
// a task (docs/moct-seam.md, failure mode).
var ErrUnavailable = errors.New("moct-be unavailable")

// APIError is a non-2xx answer from moct-be, mirroring its
// {ok:false,code,error} envelope. A 4xx means our request was wrong; check
// Code for moct-be's machine-readable reason.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("moct: HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("moct: HTTP %d: %s", e.StatusCode, e.Message)
}
