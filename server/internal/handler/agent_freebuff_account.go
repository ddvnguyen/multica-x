package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Per-agent Freebuff (CLI) account pick, stored in the agent's
// runtime_config (M-165):
//
//	{"freebuff": {"account_id": "<account id or empty>"}}
//
// The daemon turns a non-empty pick into FREEBUFF_ACCOUNT for the run,
// layered under the agent's custom_env. These helpers validate the pick at
// agent-write time against the runtime the agent will run on: the runtime
// must be a Freebuff runtime and the account must be one the daemon
// reported for it (the list lives in the runtime row's metadata.freebuff).
// Moving an agent to a runtime that cannot honour the pick strips it, the
// same reset rule the freebuff-desktop account columns follow.

// freebuffAccountPick is the pinned account id extracted from runtime_config.
type freebuffAccountPick struct {
	ID      string
	HasPick bool
}

func freebuffAccountPickFromRuntimeConfig(rc []byte) freebuffAccountPick {
	if len(rc) == 0 {
		return freebuffAccountPick{}
	}
	var cfg struct {
		Freebuff *struct {
			AccountID string `json:"account_id"`
		} `json:"freebuff"`
	}
	if err := json.Unmarshal(rc, &cfg); err != nil || cfg.Freebuff == nil {
		return freebuffAccountPick{}
	}
	return freebuffAccountPick{
		ID:      strings.TrimSpace(cfg.Freebuff.AccountID),
		HasPick: true,
	}
}

// validateFreebuffAccountPick checks a runtime_config blob against the
// runtime row it will run on. It returns an HTTP status and message, or 0
// when the pick is valid — including "no pick".
func validateFreebuffAccountPick(runtime db.AgentRuntime, rc []byte) (int, string) {
	pick := freebuffAccountPickFromRuntimeConfig(rc)
	if !pick.HasPick || pick.ID == "" {
		return 0, ""
	}
	if runtime.Provider != FreebuffProvider {
		return http.StatusBadRequest, "a Freebuff account selection is only supported on Freebuff runtimes"
	}
	if freebuffBlockFromRuntime(runtime).find(pick.ID) < 0 {
		return http.StatusBadRequest, "account not found on this runtime"
	}
	return 0, ""
}

// stripFreebuffAccountPick removes the freebuff block from a runtime_config
// blob. It reports whether the blob changed; an unparsable blob is returned
// unchanged (the server stores it as-is, as it already does for any JSON).
func stripFreebuffAccountPick(rc []byte) ([]byte, bool) {
	if len(rc) == 0 {
		return rc, false
	}
	var m map[string]any
	if err := json.Unmarshal(rc, &m); err != nil {
		return rc, false
	}
	if _, ok := m["freebuff"]; !ok {
		return rc, false
	}
	delete(m, "freebuff")
	out, err := json.Marshal(m)
	if err != nil {
		return rc, false
	}
	return out, true
}
