package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// claimPauseOwner records which subsystem currently holds the shared claim
// barrier (pauseClaims). Auto-update and the deploy drain lease both need to
// stop new claims, but each is only allowed to clear its own hold.
type claimPauseOwner uint8

const (
	claimPauseNone claimPauseOwner = iota
	claimPauseUpdate
	claimPauseDrain
)

const (
	drainStateDraining = "draining"
	drainStateDrained  = "drained"
	drainStateAborted  = "aborted"
	drainStateExpired  = "expired"

	drainControlFileName = "daemon-drain.json"
	drainTokenBytes      = 24
	drainLeaseTick       = 10 * time.Millisecond
)

var (
	errDrainBusy          = errors.New("claim barrier already held (another drain or an update)")
	errDrainNotFound      = errors.New("no drain lease")
	errDrainTokenMismatch = errors.New("drain lease token mismatch")
)

// drainLeaseState is the live lease, guarded by Daemon.claimMu.
type drainLeaseState struct {
	token         string
	state         string
	expiresAt     time.Time
	actor         string
	workflowRunID string
	unit          string
	force         bool
	timeout       time.Duration
	stop          chan struct{}
}

// drainLeaseView is both the JSON response and the state a caller can act on.
type drainLeaseView struct {
	Status          string `json:"status"`
	Token           string `json:"token,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	ActiveTaskCount int64  `json:"active_task_count"`
	ClaimsInFlight  int    `json:"claims_in_flight"`
	Actor           string `json:"actor,omitempty"`
	WorkflowRunID   string `json:"workflow_run_id,omitempty"`
	Unit            string `json:"unit,omitempty"`
	Force           bool   `json:"force,omitempty"`
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"`
}

// drainControlToken is the process-local credential that authorizes use of
// the drain endpoints. It is published to a 0600 file that only the OS user
// running the daemon can read; the listener itself is bound to 127.0.0.1.
var drainControlToken atomic.Pointer[string]

// publishDrainControl generates a fresh control credential for this process
// and writes it together with the actual health port to a 0600 file. The
// deploy script reads that file instead of guessing the profile-derived port,
// so a port collision or a port-0 test listener cannot make it talk to the
// wrong daemon. A stale file from a previous process is overwritten: the
// daemon that wrote it last is the only one whose token is accepted.
func (d *Daemon) publishDrainControl(port int) error {
	token, err := randomDrainToken()
	if err != nil {
		return err
	}
	path := d.cfg.DrainControlFile
	if path == "" {
		dir, dirErr := cli.ProfileDir(d.cfg.Profile)
		if dirErr != nil {
			return fmt.Errorf("resolve drain control directory: %w", dirErr)
		}
		path = filepath.Join(dir, drainControlFileName)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create drain control directory: %w", err)
	}
	body, err := json.Marshal(struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}{Port: port, Token: token})
	if err != nil {
		return err
	}
	// Write to a sibling temp file and rename so a concurrent reader never
	// sees a half-written credential.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-drain-*.tmp")
	if err != nil {
		return fmt.Errorf("create drain control temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod drain control file: %w", err)
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write drain control file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close drain control file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install drain control file: %w", err)
	}
	// Publish the credential only after the file is readable, so a caller can
	// never hold a token that the published file does not yet describe.
	controlToken := token
	drainControlToken.Store(&controlToken)
	return nil
}

func randomDrainToken() (string, error) {
	buf := make([]byte, drainTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate drain token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func drainTokenEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// beginDrainLease atomically pauses all claim paths and creates a uniquely
// owned lease. The pause is only taken if the daemon is not already paused by
// another barrier; auto-update's trySetClaimBarrier / tryBeginServerUpdate use
// the same check and therefore defer for the lifetime of the lease.
func (d *Daemon) beginDrainLease(ttl time.Duration, actor, workflowRunID, unit string, force bool) (drainLeaseView, error) {
	if ttl <= 0 {
		return drainLeaseView{}, fmt.Errorf("drain timeout must be positive")
	}
	token, err := randomDrainToken()
	if err != nil {
		return drainLeaseView{}, err
	}
	expiresAt := time.Now().UTC().Add(ttl)

	d.claimMu.Lock()
	if d.pauseClaims || (d.drainLease != nil &&
		(d.drainLease.state == drainStateDraining || d.drainLease.state == drainStateDrained)) {
		d.claimMu.Unlock()
		return drainLeaseView{}, errDrainBusy
	}
	d.pauseClaims = true
	d.claimPauseOwner = claimPauseDrain
	lease := &drainLeaseState{
		token:         token,
		state:         drainStateDraining,
		expiresAt:     expiresAt,
		actor:         actor,
		workflowRunID: workflowRunID,
		unit:          unit,
		force:         force,
		timeout:       ttl,
		stop:          make(chan struct{}),
	}
	d.drainLease = lease
	view := drainLeaseViewFrom(lease, d.claimsInFlight, d.activeTasks.Load())
	d.claimMu.Unlock()

	d.logger.Info("drain lease acquired",
		"operation", "drain_begin",
		"actor", actor,
		"workflow_run_id", workflowRunID,
		"unit", unit,
		"force", force,
		"timeout_seconds", int(ttl/time.Second),
		"outcome", drainStateDraining,
		"expires_at_utc", expiresAt.Format(time.RFC3339),
	)
	go d.watchDrainLease(token, lease.stop)
	return view, nil
}

// watchDrainLease waits for claims and running tasks to reach zero, then
// marks the lease drained while leaving the claim barrier held. A timeout or
// abort releases the barrier so the daemon resumes claiming on its own if the
// deploy script loses control.
func (d *Daemon) watchDrainLease(token string, stop <-chan struct{}) {
	ticker := time.NewTicker(drainLeaseTick)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		d.claimMu.Lock()
		lease := d.drainLease
		if lease == nil || lease.token != token ||
			(lease.state != drainStateDraining && lease.state != drainStateDrained) {
			d.claimMu.Unlock()
			return
		}
		if lease.state == drainStateDraining {
			if lease.expiresAt.Before(time.Now().UTC()) {
				lease.state = drainStateExpired
				d.clearClaimPauseIfOwner(claimPauseDrain)
				d.claimMu.Unlock()
				d.logger.Warn("drain lease expired; claims resumed",
					"operation", "drain_expire",
					"actor", lease.actor,
					"workflow_run_id", lease.workflowRunID,
					"unit", lease.unit,
					"force", lease.force,
					"timeout_seconds", int(lease.timeout/time.Second),
					"outcome", drainStateExpired,
				)
				return
			}
			if d.claimsInFlight == 0 && d.activeTasks.Load() == 0 {
				lease.state = drainStateDrained
				d.claimMu.Unlock()
				d.logger.Info("drain lease idle; claims remain paused",
					"operation", "drain_idle",
					"workflow_run_id", lease.workflowRunID,
					"outcome", drainStateDrained,
				)
				continue
			}
		}
		d.claimMu.Unlock()
	}
}

func drainLeaseViewFrom(lease *drainLeaseState, claimsInFlight int, activeTasks int64) drainLeaseView {
	return drainLeaseView{
		Status:          lease.state,
		Token:           lease.token,
		ExpiresAt:       lease.expiresAt.UTC().Format(time.RFC3339),
		ActiveTaskCount: activeTasks,
		ClaimsInFlight:  claimsInFlight,
		Actor:           lease.actor,
		WorkflowRunID:   lease.workflowRunID,
		Unit:            lease.unit,
		Force:           lease.force,
		TimeoutSeconds:  int(lease.timeout / time.Second),
	}
}

// drainLeaseStatus returns the state of the lease owned by token, including
// terminal states (aborted/expired) so a deploy can tell a lost lease from a
// missing one. Only the holder of the lease token may read it, so a stale
// deploy cannot observe or abort a newer lease.
func (d *Daemon) drainLeaseStatus(token string) (drainLeaseView, error) {
	d.claimMu.Lock()
	defer d.claimMu.Unlock()
	lease := d.drainLease
	if lease == nil {
		return drainLeaseView{}, errDrainNotFound
	}
	if !drainTokenEqual(lease.token, token) {
		return drainLeaseView{}, errDrainTokenMismatch
	}
	return drainLeaseViewFrom(lease, d.claimsInFlight, d.activeTasks.Load()), nil
}

// abortDrainLease releases the claim pause and marks the lease aborted. Used
// when a deploy cannot confirm it still controls the drain (lost token, lost
// health endpoint, timeout without force).
func (d *Daemon) abortDrainLease(token string) (drainLeaseView, error) {
	d.claimMu.Lock()
	lease := d.drainLease
	if lease == nil {
		d.claimMu.Unlock()
		return drainLeaseView{}, errDrainNotFound
	}
	if !drainTokenEqual(lease.token, token) {
		d.claimMu.Unlock()
		return drainLeaseView{}, errDrainTokenMismatch
	}
	// Abort is idempotent for a lease that already expired or was aborted:
	// the deploy only needs the claims to be resumed, which they already are.
	if lease.state == drainStateAborted || lease.state == drainStateExpired {
		view := drainLeaseViewFrom(lease, d.claimsInFlight, d.activeTasks.Load())
		d.claimMu.Unlock()
		return view, nil
	}
	lease.state = drainStateAborted
	d.clearClaimPauseIfOwner(claimPauseDrain)
	close(lease.stop)
	view := drainLeaseViewFrom(lease, d.claimsInFlight, d.activeTasks.Load())
	d.claimMu.Unlock()

	d.logger.Info("drain lease aborted; claims resumed",
		"operation", "drain_abort",
		"actor", lease.actor,
		"workflow_run_id", lease.workflowRunID,
		"unit", lease.unit,
		"force", lease.force,
		"timeout_seconds", int(lease.timeout/time.Second),
		"outcome", drainStateAborted,
	)
	return view, nil
}

// drainBeginRequest is the body of POST /drain/begin.
type drainBeginRequest struct {
	Actor         string `json:"actor"`
	WorkflowRunID string `json:"workflow_run_id"`
	Unit          string `json:"unit"`
	Force         bool   `json:"force"`
	Timeout       int    `json:"timeout_seconds"`
}

// drainAuthorized checks the process-local control credential. The listener
// is loopback-only; this token stops any other local process from pausing
// claims.
func drainAuthorized(r *http.Request) bool {
	const bearer = "Bearer "
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, bearer) {
		return false
	}
	provided := strings.TrimSpace(header[len(bearer):])
	expected := drainControlToken.Load()
	if expected == nil {
		return false
	}
	return drainTokenEqual(provided, *expected)
}

// drainHandler serves the local, authenticated drain control endpoints.
func (d *Daemon) drainHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !drainAuthorized(r) {
			http.Error(w, "unauthorized drain control", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/drain/begin":
			var req drainBeginRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if req.Timeout <= 0 || req.Timeout > 24*60*60 {
				http.Error(w, "timeout_seconds must be between 1 and 86400", http.StatusBadRequest)
				return
			}
			view, err := d.beginDrainLease(
				time.Duration(req.Timeout)*time.Second,
				req.Actor, req.WorkflowRunID, req.Unit, req.Force,
			)
			if err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, errDrainBusy) {
					status = http.StatusConflict
				}
				http.Error(w, err.Error(), status)
				return
			}
			json.NewEncoder(w).Encode(view)

		case r.Method == http.MethodGet && r.URL.Path == "/drain/status",
			r.Method == http.MethodPost && r.URL.Path == "/drain/status":
			token := strings.TrimSpace(r.Header.Get("X-Multica-Drain-Token"))
			if token == "" {
				http.Error(w, "drain token required", http.StatusBadRequest)
				return
			}
			view, err := d.drainLeaseStatus(token)
			if err != nil {
				d.writeDrainLeaseError(w, err)
				return
			}
			json.NewEncoder(w).Encode(view)

		case r.Method == http.MethodPost && r.URL.Path == "/drain/abort":
			token := strings.TrimSpace(r.Header.Get("X-Multica-Drain-Token"))
			if token == "" {
				http.Error(w, "drain token required", http.StatusBadRequest)
				return
			}
			view, err := d.abortDrainLease(token)
			if err != nil {
				d.writeDrainLeaseError(w, err)
				return
			}
			json.NewEncoder(w).Encode(view)

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}
}

func (d *Daemon) writeDrainLeaseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDrainNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, errDrainTokenMismatch):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
