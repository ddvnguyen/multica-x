package daemon

import (
	"encoding/json"
	"strings"
)

// freebuffRuntimeAccount extracts the freebuff-specific account pin from an
// agent's runtime_config payload (M-165):
//
//	{"freebuff": {"account_id": "<account id or empty>"}}
//
// An empty, absent or malformed block yields "" — the run then uses the
// runtime's default account, exactly as before the pick existed. A malformed
// payload degrades instead of failing dispatch, mirroring
// decodeOpenclawRuntimeConfig: one bad save must not block every task the
// agent runs. The server validates the id at agent-write time; the account
// may still disappear in between, in which case the run fails with the
// backend's scrutible "no Freebuff account" error rather than a silent
// fallback.
func freebuffRuntimeAccount(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var cfg struct {
		Freebuff struct {
			AccountID string `json:"account_id"`
		} `json:"freebuff"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Freebuff.AccountID)
}
