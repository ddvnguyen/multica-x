package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SetFreebuffDesktopDefaultAccount pins which account a freebuff-desktop
// runtime's `account_mode = default` agents fall back to (M-203).
//
// The pin lives in the runtime's own Freebuff block
// (`metadata.freebuff.default_account_id`), so it survives daemon reports and
// needs no new column. An empty `account_id` clears it and those agents go back
// to the runtime's own configuration.
//
// Only an account this runtime can actually run is accepted: enabled,
// freebuff-desktop provider, and — for a daemon-managed account — owned by this
// runtime and connected. The claim path re-checks every one of those rules, so
// this is the write-side half of the same contract, not a second opinion.
func (h *Handler) SetFreebuffDesktopDefaultAccount(w http.ResponseWriter, r *http.Request) {
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	if rt.Provider != accountRuntimeProvider {
		writeError(w, http.StatusNotFound, "runtime does not run freebuff-desktop accounts")
		return
	}

	var req struct {
		AccountID string `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	accountID := strings.TrimSpace(req.AccountID)
	if accountID != "" {
		id, err := util.ParseUUID(accountID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid account_id")
			return
		}
		acc, gerr := h.Queries.GetWorkspaceAccountInWorkspace(r.Context(), db.GetWorkspaceAccountInWorkspaceParams{
			ID: id, WorkspaceID: rt.WorkspaceID,
		})
		if gerr != nil {
			writeError(w, http.StatusBadRequest, "account not found in workspace")
			return
		}
		if acc.Provider != accountProviderFreebuffDesktop {
			writeError(w, http.StatusBadRequest, "account provider mismatch")
			return
		}
		if !acc.Enabled {
			writeError(w, http.StatusBadRequest, "account is disabled")
			return
		}
		if acc.RuntimeID.Valid && acc.RuntimeID != rt.ID {
			writeError(w, http.StatusBadRequest, "account belongs to another runtime")
			return
		}
		if acc.DaemonAccountID.Valid && acc.State != "connected" {
			writeError(w, http.StatusBadRequest, "managed account is not connected")
			return
		}
		accountID = util.UUIDToString(id)
	}

	block, err := h.updateFreebuffBlock(r, rt, func(b *FreebuffBlock) (bool, error) {
		if b.DefaultAccountID == accountID {
			return false, nil
		}
		b.DefaultAccountID = accountID
		return true, nil
	})
	if err != nil {
		slog.Warn("freebuff-desktop set default account failed", "runtime_id", uuidToString(rt.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to set the default account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"default_account_id": block.DefaultAccountID})
}
