package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type AgentPresetResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	TierID      string `json:"tier_id"`
	TierName    string `json:"tier_name"`
	TierEnabled bool   `json:"tier_enabled"`
}

type agentPresetInput struct {
	Name    string `json:"name"`
	AgentID string `json:"agent_id"`
	TierID  string `json:"tier_id"`
}

func presetName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", errors.New("name must be 1-80 characters")
	}
	return name, nil
}

func presetConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (h *Handler) agentPresetFeatureEnabled(ctx context.Context, workspaceID pgtype.UUID) (bool, error) {
	var enabled bool
	err := h.DB.QueryRow(ctx, `SELECT settings @> '{"agent_presets_enabled":true}'::jsonb FROM workspace WHERE id = $1`, workspaceID).Scan(&enabled)
	return enabled, err
}

func (h *Handler) ListAgentPresets(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	enabled, err := h.agentPresetFeatureEnabled(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read preset setting")
		return
	}
	if !enabled {
		writeJSON(w, http.StatusOK, map[string]any{"presets": []AgentPresetResponse{}})
		return
	}

	rows, err := h.DB.Query(r.Context(), `
		SELECT p.id, p.workspace_id, p.name, p.agent_id, a.name, p.tier_id,
		       COALESCE(t.name, ''), COALESCE(t.enabled, false)
		FROM agent_preset p
		JOIN agent a ON a.id = p.agent_id AND a.workspace_id = p.workspace_id
		LEFT JOIN model_tier t ON t.id = p.tier_id AND t.workspace_id = p.workspace_id
		WHERE p.workspace_id = $1
		ORDER BY lower(p.name), p.id`, workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list presets")
		return
	}
	defer rows.Close()

	out := make([]AgentPresetResponse, 0)
	for rows.Next() {
		var preset AgentPresetResponse
		if err := rows.Scan(
			&preset.ID, &preset.WorkspaceID, &preset.Name, &preset.AgentID,
			&preset.AgentName, &preset.TierID, &preset.TierName, &preset.TierEnabled,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read presets")
			return
		}
		out = append(out, preset)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "failed to read presets")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": out})
}

func (h *Handler) CreateAgentPreset(w http.ResponseWriter, r *http.Request) {
	h.saveAgentPreset(w, r, false)
}

func (h *Handler) UpdateAgentPreset(w http.ResponseWriter, r *http.Request) {
	h.saveAgentPreset(w, r, true)
}

func (h *Handler) saveAgentPreset(w http.ResponseWriter, r *http.Request, update bool) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	enabled, err := h.agentPresetFeatureEnabled(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read preset setting")
		return
	}
	if !enabled {
		writeError(w, http.StatusNotFound, "presets are not enabled")
		return
	}

	var input agentPresetInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := presetName(input.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, input.AgentID, "agent_id")
	if !ok {
		return
	}
	tierID, ok := parseUUIDOrBadRequest(w, input.TierID, "tier_id")
	if !ok {
		return
	}
	var targetsExist bool
	err = h.DB.QueryRow(r.Context(), `
		SELECT EXISTS (
		    SELECT 1 FROM agent WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL
		) AND EXISTS (
		    SELECT 1 FROM model_tier WHERE id = $3 AND workspace_id = $2
		)`, agentID, workspaceUUID, tierID).Scan(&targetsExist)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate preset targets")
		return
	}
	if !targetsExist {
		writeError(w, http.StatusBadRequest, "agent and tier must belong to this workspace")
		return
	}

	var row pgx.Row
	if update {
		id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "presetId"), "preset id")
		if !ok {
			return
		}
		row = h.DB.QueryRow(r.Context(), `
			UPDATE agent_preset
			SET name = $3, agent_id = $4, tier_id = $5, updated_at = now()
			WHERE id = $1 AND workspace_id = $2
			RETURNING id, workspace_id, name, agent_id, tier_id`,
			id, workspaceUUID, name, agentID, tierID)
	} else {
		row = h.DB.QueryRow(r.Context(), `
			INSERT INTO agent_preset (workspace_id, name, agent_id, tier_id)
			VALUES ($1, $2, $3, $4)
			RETURNING id, workspace_id, name, agent_id, tier_id`,
			workspaceUUID, name, agentID, tierID)
	}

	var id, savedWorkspaceID, savedAgentID, savedTierID pgtype.UUID
	var savedName string
	if err := row.Scan(&id, &savedWorkspaceID, &savedName, &savedAgentID, &savedTierID); err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "preset not found")
		case presetConflict(err):
			writeError(w, http.StatusConflict, "a preset with this name already exists")
		default:
			writeError(w, http.StatusInternalServerError, "failed to save preset")
		}
		return
	}

	status := http.StatusOK
	if !update {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]string{
		"id":          uuidToString(id),
		"workspace_id": uuidToString(savedWorkspaceID),
		"name":         savedName,
		"agent_id":     uuidToString(savedAgentID),
		"tier_id":      uuidToString(savedTierID),
	})
}

func (h *Handler) DeleteAgentPreset(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	enabled, err := h.agentPresetFeatureEnabled(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read preset setting")
		return
	}
	if !enabled {
		writeError(w, http.StatusNotFound, "presets are not enabled")
		return
	}
	presetID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "presetId"), "preset id")
	if !ok {
		return
	}
	tag, err := h.DB.Exec(r.Context(), `DELETE FROM agent_preset WHERE id = $1 AND workspace_id = $2`, presetID, workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete preset")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "preset not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
