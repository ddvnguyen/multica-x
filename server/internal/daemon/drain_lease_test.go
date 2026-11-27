package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The tests below drive a throwaway Daemon and its real loopback-control
// handlers. Nothing touches multica-daemon.service on the host.

func drainTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := freshDaemon("http://127.0.0.1:0")
	d.cfg.DrainControlFile = filepath.Join(t.TempDir(), "daemon-drain.json")
	if err := d.publishDrainControl(19514); err != nil {
		t.Fatalf("publishDrainControl: %v", err)
	}
	t.Cleanup(func() { drainControlToken.Store(nil) })
	return d
}

func drainControlTokenFromFile(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat drain control file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("drain control file mode = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read drain control file: %v", err)
	}
	var control struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatalf("decode drain control file: %v", err)
	}
	if control.Port != 19514 || control.Token == "" {
		t.Fatalf("drain control = %+v, want port 19514 and a token", control)
	}
	return control.Token
}

func drainServer(t *testing.T, d *Daemon) (string, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/drain/", d.drainHandler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, drainControlTokenFromFile(t, d.cfg.DrainControlFile)
}

func drainDo(t *testing.T, method, url, token, leaseToken string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if leaseToken != "" {
		req.Header.Set("X-Multica-Drain-Token", leaseToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, raw
}

func drainBeginHTTP(t *testing.T, base, token string, timeout int, force bool) drainLeaseView {
	t.Helper()
	status, raw := drainDo(t, http.MethodPost, base+"/drain/begin", token, "", drainBeginRequest{
		Actor: "deploy-test", WorkflowRunID: "run-1", Unit: "multica-daemon.service",
		Force: force, Timeout: timeout,
	})
	if status != http.StatusOK {
		t.Fatalf("POST /drain/begin status = %d, want 200: %s", status, raw)
	}
	var view drainLeaseView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode begin response: %v", err)
	}
	if view.Token == "" || view.Status != drainStateDraining {
		t.Fatalf("begin response = %+v, want token and draining", view)
	}
	return view
}

func drainStatusHTTP(t *testing.T, base, token, leaseToken string) drainLeaseView {
	t.Helper()
	status, raw := drainDo(t, http.MethodGet, base+"/drain/status", token, leaseToken, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /drain/status status = %d, want 200: %s", status, raw)
	}
	var view drainLeaseView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	return view
}

func waitForDrainState(t *testing.T, d *Daemon, token, want string) drainLeaseView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err := d.drainLeaseStatus(token)
		if err == nil && view.Status == want {
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("drain lease %s never reached %s", token, want)
	return drainLeaseView{}
}

func TestDrainControl_IsAuthenticated(t *testing.T) {
	d := drainTestDaemon(t)
	base, token := drainServer(t, d)

	// No credential at all: the endpoint must refuse to pause claims.
	status, _ := drainDo(t, http.MethodPost, base+"/drain/begin", "", "", drainBeginRequest{Timeout: 2})
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthorized begin status = %d, want 401", status)
	}
	// A wrong credential is refused too.
	status, _ = drainDo(t, http.MethodPost, base+"/drain/begin", "not-the-token", "", drainBeginRequest{Timeout: 2})
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong-token begin status = %d, want 401", status)
	}
	d.claimMu.Lock()
	paused := d.pauseClaims
	d.claimMu.Unlock()
	if paused {
		t.Fatal("a rejected request paused claims")
	}

	// The credential published to the 0600 file works.
	_ = drainBeginHTTP(t, base, token, 2, false)
}

func TestDrainLease_ActiveTaskDrainEndToEnd(t *testing.T) {
	d := drainTestDaemon(t)
	base, token := drainServer(t, d)

	d.activeTasks.Store(1)
	view := drainBeginHTTP(t, base, token, 2, false)
	if view.ActiveTaskCount != 1 {
		t.Fatalf("active count at begin = %d, want 1", view.ActiveTaskCount)
	}

	// A task is running: the lease must wait rather than declare idle.
	statusView := drainStatusHTTP(t, base, token, view.Token)
	if statusView.Status != drainStateDraining {
		t.Fatalf("status with active task = %s, want draining", statusView.Status)
	}
	if d.tryEnterClaim() {
		t.Fatal("claims resumed while a drain lease held the barrier")
		d.exitClaim()
	}

	// Once the task returns, the lease reaches drained and keeps claims paused
	// through the (simulated) stop.
	d.activeTasks.Store(0)
	waitForDrainState(t, d, view.Token, drainStateDrained)
	if view := drainStatusHTTP(t, base, token, view.Token); view.ActiveTaskCount != 0 {
		t.Fatalf("drained status active count = %d, want 0", view.ActiveTaskCount)
	}
	if d.tryEnterClaim() {
		t.Fatal("claims resumed before the daemon was stopped")
	}
	d.exitClaim()

	status, raw := drainDo(t, http.MethodPost, base+"/drain/abort", token, view.Token, nil)
	if status != http.StatusOK {
		t.Fatalf("abort status = %d, want 200: %s", status, raw)
	}
	if !d.tryEnterClaim() {
		t.Fatal("claims did not resume after abort")
	}
	d.exitClaim()
}

func TestDrainLease_ClaimInFlightRace(t *testing.T) {
	d := drainTestDaemon(t)
	base, token := drainServer(t, d)

	// A poller decided to claim but has not handed the task off yet.
	if !d.tryEnterClaim() {
		t.Fatal("tryEnterClaim should succeed before the lease is taken")
	}
	view := drainBeginHTTP(t, base, token, 2, false)
	if view.ClaimsInFlight != 1 || view.Status != drainStateDraining {
		t.Fatalf("begin with claim in flight = %+v, want draining/1", view)
	}
	if got := drainStatusHTTP(t, base, token, view.Token); got.Status != drainStateDraining {
		t.Fatalf("status with claim in flight = %s, want draining", got.Status)
	}

	// The handoff increments activeTasks before exitClaim; here we simulate the
	// empty-claim half of the race, which must let the lease reach drained.
	d.exitClaim()
	waitForDrainState(t, d, view.Token, drainStateDrained)
	if _, err := d.abortDrainLease(view.Token); err != nil {
		t.Fatalf("abort: %v", err)
	}
}

func TestDrainLease_ExpiryResumesClaims(t *testing.T) {
	d := drainTestDaemon(t)
	d.activeTasks.Store(1)
	view, err := d.beginDrainLease(60*time.Millisecond, "deploy-test", "run-1", "multica-daemon.service", false)
	if err != nil {
		t.Fatalf("beginDrainLease: %v", err)
	}
	if d.tryEnterClaim() {
		t.Fatal("claims resumed while lease was active")
		d.exitClaim()
	}

	waitForDrainState(t, d, view.Token, drainStateExpired)
	if !d.tryEnterClaim() {
		t.Fatal("claims did not resume after lease expiry")
	}
	d.exitClaim()
	// A new lease may be taken once the old one expired.
	if _, err := d.beginDrainLease(time.Second, "deploy-test", "run-2", "", false); err != nil {
		t.Fatalf("begin after expiry: %v", err)
	}
}

func TestDrainLease_UniquelyOwnedAndAbortResumesClaims(t *testing.T) {
	d := drainTestDaemon(t)
	view, err := d.beginDrainLease(time.Second, "deploy-test", "run-1", "multica-daemon.service", false)
	if err != nil {
		t.Fatalf("beginDrainLease: %v", err)
	}

	// A second owner cannot take the lease while the first holds it.
	if _, err := d.beginDrainLease(time.Second, "other", "run-2", "", false); err == nil {
		t.Fatal("second begin succeeded while a lease was active")
	}
	// A wrong token cannot abort someone else's lease.
	if _, err := d.abortDrainLease("wrong-token"); err == nil {
		t.Fatal("abort with wrong token succeeded")
	}
	if !d.pauseClaims {
		t.Fatal("wrong-token abort released the claim barrier")
	}
	// Worst case: the deploy loses control entirely. Update cleanup must not
	// clear a pause it does not own, and the barrier must still refuse claims.
	d.releaseClaimBarrier()
	if !d.pauseClaims {
		t.Fatal("auto-update cleanup cleared the drain lease's claim barrier")
	}
	if d.trySetClaimBarrier() {
		t.Fatal("auto-update acquired the barrier during a drain lease")
		d.releaseClaimBarrier()
	}

	if _, err := d.abortDrainLease(view.Token); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if d.pauseClaims || d.claimPauseOwner != claimPauseNone {
		t.Fatal("claim barrier not released on abort")
	}
	// A fresh lease is now possible.
	fresh, err := d.beginDrainLease(time.Second, "other", "run-2", "", true)
	if err != nil {
		t.Fatalf("begin after abort: %v", err)
	}
	if !fresh.Force {
		t.Fatal("forced-cancellation flag was not preserved on the lease")
	}
	if _, err := d.abortDrainLease(fresh.Token); err != nil {
		t.Fatalf("cleanup abort: %v", err)
	}
}

func TestTrySetClaimBarrier_BlocksDrainBegin(t *testing.T) {
	d := drainTestDaemon(t)
	if !d.trySetClaimBarrier() {
		t.Fatal("trySetClaimBarrier should succeed while idle")
	}
	if _, err := d.beginDrainLease(time.Second, "deploy-test", "run-1", "", false); err == nil {
		d.releaseClaimBarrier()
		t.Fatal("drain lease acquired while auto-update held the barrier")
	}
	d.releaseClaimBarrier()
	if _, err := d.beginDrainLease(time.Second, "deploy-test", "run-1", "", false); err != nil {
		t.Fatalf("begin after update released its barrier: %v", err)
	}
}

func TestDrainBeginRequest_RejectsBadTimeout(t *testing.T) {
	d := drainTestDaemon(t)
	base, token := drainServer(t, d)
	for _, timeout := range []int{0, -1, 86401} {
		status, _ := drainDo(t, http.MethodPost, base+"/drain/begin", token, "",
			drainBeginRequest{Timeout: timeout})
		if status != http.StatusBadRequest {
			t.Fatalf("timeout %d status = %d, want 400", timeout, status)
		}
	}
}
