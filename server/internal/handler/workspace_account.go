package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type WorkspaceAccountResponse struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	Endpoint      string `json:"endpoint,omitempty"`
	Enabled       bool   `json:"enabled"`
	CredentialRef string `json:"credential_ref,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func workspaceAccountToResponse(a db.WorkspaceAccount, includeSecret bool) WorkspaceAccountResponse {
	resp := WorkspaceAccountResponse{
		ID:          uuidToString(a.ID),
		WorkspaceID: uuidToString(a.WorkspaceID),
		Provider:    a.Provider,
		Label:       a.Label,
		Enabled:     a.Enabled,
		CreatedAt:   timestampToString(a.CreatedAt),
		UpdatedAt:   timestampToString(a.UpdatedAt),
	}
	if a.Endpoint.Valid {
		resp.Endpoint = a.Endpoint.String
	}
	if includeSecret && a.CredentialRef.Valid {
		resp.CredentialRef = a.CredentialRef.String
	}
	return resp
}

func (h *Handler) ListWorkspaceAccounts(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	accounts, err := h.Queries.ListWorkspaceAccounts(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list accounts")
		return
	}
	out := make([]WorkspaceAccountResponse, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, workspaceAccountToResponse(a, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (h *Handler) CreateWorkspaceAccount(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	// admin/owner only
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}

	var req struct {
		Provider      string `json:"provider"`
		Label         string `json:"label"`
		Endpoint      string `json:"endpoint"`
		CredentialRef string `json:"credential_ref"`
		Enabled       *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Provider) == "" {
		req.Provider = "freebuff-desktop"
	}
	if strings.TrimSpace(req.Label) == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ownerID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	created, err := h.Queries.CreateWorkspaceAccount(r.Context(), db.CreateWorkspaceAccountParams{
		WorkspaceID:   wsUUID,
		Provider:      strings.TrimSpace(req.Provider),
		Label:         strings.TrimSpace(req.Label),
		Endpoint:      pgtype.Text{String: strings.TrimSpace(req.Endpoint), Valid: strings.TrimSpace(req.Endpoint) != ""},
		CredentialRef: pgtype.Text{String: strings.TrimSpace(req.CredentialRef), Valid: strings.TrimSpace(req.CredentialRef) != ""},
		Enabled:       enabled,
		CreatedBy:     parseUUIDOrNil(ownerID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create account")
		return
	}
	writeJSON(w, http.StatusCreated, workspaceAccountToResponse(created, true))
}

func (h *Handler) UpdateWorkspaceAccount(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	accountID := strings.TrimSpace(chi.URLParam(r, "accountId"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	accUUID, ok := parseUUIDOrBadRequest(w, accountID, "account id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetWorkspaceAccountInWorkspace(r.Context(), db.GetWorkspaceAccountInWorkspaceParams{
		ID:          accUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}

	var req struct {
		Label         *string `json:"label"`
		Endpoint      *string `json:"endpoint"`
		CredentialRef *string `json:"credential_ref"`
		Enabled       *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var endpoint, cred pgtype.Text
	endpoint = existing.Endpoint
	cred = existing.CredentialRef
	if req.Endpoint != nil {
		endpoint = pgtype.Text{String: strings.TrimSpace(*req.Endpoint), Valid: strings.TrimSpace(*req.Endpoint) != ""}
	}
	if req.CredentialRef != nil {
		cred = pgtype.Text{String: strings.TrimSpace(*req.CredentialRef), Valid: strings.TrimSpace(*req.CredentialRef) != ""}
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	label := existing.Label
	if req.Label != nil {
		label = strings.TrimSpace(*req.Label)
	}

	updated, err := h.Queries.UpdateWorkspaceAccount(r.Context(), db.UpdateWorkspaceAccountParams{
		ID:            existing.ID,
		Label:         label,
		Endpoint:      endpoint,
		CredentialRef: cred,
		Enabled:       enabled,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update account")
		return
	}
	_ = wsUUID
	writeJSON(w, http.StatusOK, workspaceAccountToResponse(updated, true))
}

func (h *Handler) DeleteWorkspaceAccount(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	accountID := strings.TrimSpace(chi.URLParam(r, "accountId"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	accUUID, ok := parseUUIDOrBadRequest(w, accountID, "account id")
	if !ok {
		return
	}
	// verify belongs to workspace FIRST (no cross-workspace tampering)
	existing, err := h.Queries.GetWorkspaceAccountInWorkspace(r.Context(), db.GetWorkspaceAccountInWorkspaceParams{
		ID: accUUID, WorkspaceID: wsUUID,
	})
	if err != nil || uuidToString(existing.WorkspaceID) != wsID {
		writeError(w, http.StatusNotFound, "account not found")
		return
	}
	// demote dependant agents, then delete
	_ = h.Queries.DemoteAgentsOnAccountDelete(r.Context(), accUUID)
	if err := h.Queries.DeleteWorkspaceAccount(r.Context(), accUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
