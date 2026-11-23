package powercontext

import (
	"errors"
	"fmt"
)

// ErrUnavailable reports that the PowerContext server could not be reached
// (dial failure, timeout, reset, or a truncated read). Callers on the pre-run
// path treat it as "start the run without memory and log a warning" — memory
// never blocks a task (docs/powercontext-seam.md, failure contract).
var ErrUnavailable = errors.New("powercontext unavailable")

// APIError is a non-2xx answer from the PowerContext server. PowerContext
// answers errors with one envelope, {"error":{"code","message","details"}};
// anything else (a proxy's HTML, an empty body) still becomes an APIError
// carrying the status so a caller never loses the fact that the call failed.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("powercontext: HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("powercontext: HTTP %d: %s", e.StatusCode, e.Message)
}
