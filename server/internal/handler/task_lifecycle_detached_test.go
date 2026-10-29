package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRuntimeHasDetachedCapability(t *testing.T) {
	meta, _ := json.Marshal(map[string]any{
		"capabilities": []string{protocol.DaemonCapabilityDetachedRuntimeV1},
	})
	rt := db.AgentRuntime{Metadata: meta}
	if !runtimeHasDetachedCapability(rt) {
		t.Fatal("expected detached capability to be detected")
	}

	meta2, _ := json.Marshal(map[string]any{
		"capabilities": []string{"other-cap"},
	})
	rt2 := db.AgentRuntime{Metadata: meta2}
	if runtimeHasDetachedCapability(rt2) {
		t.Fatal("unexpected detached capability")
	}

	rt3 := db.AgentRuntime{}
	if runtimeHasDetachedCapability(rt3) {
		t.Fatal("empty metadata should not be detached")
	}
}
