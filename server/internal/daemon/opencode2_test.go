package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// TestOpencode2IdentityInDaemonWiring pins the daemon-side halves of the
// OpenCode 2 runtime identity that do not flow from the descriptor on their
// own: the display name registered into the runtime row, and the poisoned-
// session classification that decides whether a clock-skew failure is reported
// as an OpenCode problem or as a generic task failure.
func TestOpencode2IdentityInDaemonWiring(t *testing.T) {
	if got, want := providerDisplayName(opencode2RuntimeIDForTest), "OpenCode 2"; got != want {
		t.Errorf("providerDisplayName(%q) = %q, want %q", opencode2RuntimeIDForTest, got, want)
	}
	if reason, ok := classifyOpenCodeClockSkew(opencode2RuntimeIDForTest, agent.ErrOpenCodeClockSkew.Error()+": clock ahead"); !ok {
		t.Errorf("clock skew for %q was not classified as an opencode failure (reason %q)", opencode2RuntimeIDForTest, reason)
	} else if reason != FailureReasonOpenCodeClockSkew {
		t.Errorf("failure reason = %q, want %q", reason, FailureReasonOpenCodeClockSkew)
	}
	// The 1.x family keeps behaving exactly as before.
	if _, ok := classifyOpenCodeClockSkew("opencode", agent.ErrOpenCodeClockSkew.Error()); !ok {
		t.Error("open code family lost clock-skew classification")
	}
	if _, ok := classifyOpenCodeClockSkew("claude", agent.ErrOpenCodeClockSkew.Error()); ok {
		t.Error("clock-skew classification leaked to an unrelated provider")
	}
}

const opencode2RuntimeIDForTest = "opencode2"
