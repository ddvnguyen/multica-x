package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

// workspaceRegistrationFailure records why one workspace failed to register
// during startup. Keeping the underlying error (instead of collapsing it into
// errNoWorkspaceRuntimesRegistered at the log site) is what lets the startup
// retry tell a backend outage from a rejected token: an outage is retried, a
// rejection exits visibly.
type workspaceRegistrationFailure struct {
	workspaceID string
	err         error
}

// startupRegistrationError is returned by syncWorkspacesFromAPI when a startup
// round left every workspace without a runtime. It wraps
// errNoWorkspaceRuntimesRegistered so the DSH-bootstrap exception in
// startupMayProceedWithoutRuntimes keeps working, and carries the per-workspace
// causes that the retry classifier needs.
type startupRegistrationError struct {
	failures []workspaceRegistrationFailure
	count    int
}

func (e *startupRegistrationError) Error() string {
	return fmt.Sprintf("%s for any of the %d workspace(s)", errNoWorkspaceRuntimesRegistered, e.count)
}

func (e *startupRegistrationError) Unwrap() error { return errNoWorkspaceRuntimesRegistered }

// isRetryableStartupError reports whether err looks like a backend outage that
// a retry can ride out. A mixed round (one transient workspace, one rejected
// token) is NOT retryable: the permanent failure must exit visibly.
func isRetryableStartupError(err error) bool {
	if err == nil {
		return false
	}
	var startupErr *startupRegistrationError
	if errors.As(err, &startupErr) {
		if len(startupErr.failures) == 0 {
			return false
		}
		for _, failure := range startupErr.failures {
			if !isRetryableRegistrationError(failure.err) {
				return false
			}
		}
		return true
	}
	return isRetryableRegistrationError(err)
}

// isRetryableRegistrationError classifies a single registration/API error.
// Transport failures, 5xx and 408/429 are transient (see isTransientError);
// auth/config failures such as 401, 403, 400 and an unresolvable runtime set
// are permanent and must fail startup.
func isRetryableRegistrationError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoRuntimesToRegister) {
		return false
	}
	return isTransientError(err)
}

// startupErrorClass returns a sanitized, low-cardinality label for an error.
// It never includes the response body, a token, or a server URL: only the
// status code and a coarse class reach the log.
func startupErrorClass(err error) string {
	var startupErr *startupRegistrationError
	if errors.As(err, &startupErr) {
		classes := make(map[string]struct{}, len(startupErr.failures))
		for _, failure := range startupErr.failures {
			classes[registrationErrorClass(failure.err)] = struct{}{}
		}
		if len(classes) == 0 {
			return "unknown"
		}
		if len(classes) == 1 {
			for class := range classes {
				return class
			}
		}
		return "mixed"
	}
	return registrationErrorClass(err)
}

func registrationErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, ErrNoRuntimesToRegister) {
		return "no_runtimes"
	}
	var reqErr *requestError
	if errors.As(err, &reqErr) {
		switch {
		case reqErr.StatusCode >= 500:
			return "http_5xx"
		case reqErr.StatusCode == http.StatusTooManyRequests:
			return "http_429"
		case reqErr.StatusCode == http.StatusRequestTimeout:
			return "http_408"
		default:
			return fmt.Sprintf("http_%d", reqErr.StatusCode)
		}
	}
	if errors.Is(err, errInvalidResponseBody) {
		return "invalid_response"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "transport"
}

// startupErrorWorkspaces lists the workspaces whose registration failed. A
// startup without a structured failure has no workspace to report.
func startupErrorWorkspaces(err error) []string {
	var startupErr *startupRegistrationError
	if !errors.As(err, &startupErr) {
		return nil
	}
	ids := make([]string, 0, len(startupErr.failures))
	seen := make(map[string]bool, len(startupErr.failures))
	for _, failure := range startupErr.failures {
		if failure.workspaceID != "" && !seen[failure.workspaceID] {
			seen[failure.workspaceID] = true
			ids = append(ids, failure.workspaceID)
		}
	}
	return ids
}

const (
	startupRetryBaseDelay = 500 * time.Millisecond
	startupRetryMaxDelay  = 8 * time.Second
)

// boundedStartupRetryDelay is the capped exponential backoff for a 1-based
// attempt, with full jitter so daemons restarting together do not hammer the
// recovering backend in lockstep. The cap keeps each wait bounded; a persistent
// outage is retried until it clears, a permanent error arrives, or the context
// is cancelled. The CLI's readiness wait does not kill the daemon on timeout,
// so a long outage still ends with a ready daemon once the backend returns.
var boundedStartupRetryDelay = func(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 20 {
		shift = 20
	}
	delay := startupRetryBaseDelay << shift
	if delay <= 0 || delay > startupRetryMaxDelay {
		delay = startupRetryMaxDelay
	}
	half := delay / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// startupRetrySleep is the cancellation-aware sleep between retries, kept as a
// var so tests can make the wait instant without changing the backoff policy.
var startupRetrySleep = retrySleep

// syncWorkspacesForStartup runs the initial sync and rides out backend outages
// without exiting the process. Health stays "starting" (d.ready is still
// false) and no claim loop has started, so no task is claimed until
// registration succeeds. A permanent auth/config error is returned at once so
// Run fails visibly, exactly as before this retry existed.
func (d *Daemon) syncWorkspacesForStartup(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := d.syncWorkspacesFromAPI(ctx, false)
		if err == nil || d.startupMayProceedWithoutRuntimes(err) {
			return err
		}
		if !isRetryableStartupError(err) {
			return err
		}
		// The sync may have been interrupted by a shutdown; do not announce a
		// retry that the context will immediately cancel.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		delay := boundedStartupRetryDelay(attempt)
		d.logger.Warn("startup registration failed; retrying",
			"attempt", attempt,
			"error_class", startupErrorClass(err),
			"workspaces", startupErrorWorkspaces(err),
			"retry_in", delay.String(),
		)
		if sleepErr := startupRetrySleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
}
