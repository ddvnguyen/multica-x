package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// roleForbiddenCreateFields are model/runtime/account settings a Role must
// never carry: its model, runtime and thinking config come from the tier at
// enqueue time, and its identity owns no account selection (M-499 slice 1).
var roleForbiddenCreateFields = []string{
	"model", "thinking_level", "service_tier",
	"runtime_id", "runtime_config", "runtime_mode",
	"use_tiers", "tier_ids", "role",
	"account_mode", "account_id",
	"custom_env", "custom_args", "mcp_config",
	"composio_toolkit_allowlist",
}

// A Role's tier_role uses the same M-497 registry slug shape as agent.role;
// membership in the workspace registry is enforced with the Role config slice.

// hasField reports whether a JSON key was sent, regardless of its value.
func hasField(rawFields map[string]json.RawMessage, key string) bool {
	_, ok := rawFields[key]
	return ok
}

// isExplicitClear reports whether a tri-state field was sent as null or as an
// empty string. Both mean "clear the column" for subtype-only fields; for an
// omitted field the caller preserves the stored value.
func isExplicitClear(rawFields map[string]json.RawMessage, key string) bool {
	raw, ok := rawFields[key]
	if !ok {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	return bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`))
}

func firstPresentField(rawFields map[string]json.RawMessage, keys []string) string {
	for _, key := range keys {
		if hasField(rawFields, key) {
			return key
		}
	}
	return ""
}

// validateAgentTierRole enforces the M-497 registry slug shape on a Role's
// tier_role. The DB CHECK backstops it.
func validateAgentTierRole(value string) error {
	if !validTierRoleName(value) {
		return fmt.Errorf("tier_role must be a lowercase slug of at most 40 characters")
	}
	return nil
}

// requireRoleManager gates Role creation and Role-field writes to workspace
// owners/admins. Roles are Owner-managed identities; an ordinary member may
// still link their own Agent to a Role they can see.
func (h *Handler) requireRoleManager(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	_, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	return ok
}

// resolveImplementableRole validates an implements_role_id target for an
// Agent: it must be a live Role in the same workspace that the caller may see.
// No FK backs this relationship, so every write path must check it here.
func (h *Handler) resolveImplementableRole(w http.ResponseWriter, r *http.Request, workspaceID, userID, roleID string) (pgtype.UUID, bool) {
	targetUUID, ok := parseUUIDOrBadRequest(w, roleID, "implements_role_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	target, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          targetUUID,
		WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil || target.Subtype != "role" {
		writeError(w, http.StatusBadRequest, "implements_role_id must name a Role in this workspace")
		return pgtype.UUID{}, false
	}
	if target.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "implements_role_id must not name an archived Role")
		return pgtype.UUID{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), target, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this Role")
		return pgtype.UUID{}, false
	}
	return targetUUID, true
}

// createRoleAgent is the model-free create path. A Role has no runtime, model
// or account configuration — the tier stamps those onto each task at enqueue —
// so it deliberately bypasses the runtime validation of the ordinary Agent
// create path. Everything else (permissions, skills, targets, events) matches.
func (h *Handler) createRoleAgent(
	w http.ResponseWriter,
	r *http.Request,
	req CreateAgentRequest,
	rawFields map[string]json.RawMessage,
	wsUUID pgtype.UUID,
	workspaceID, ownerID string,
	conversationStarters []AgentConversationStarter,
) {
	if !h.requireRoleManager(w, r, workspaceID) {
		return
	}
	if field := firstPresentField(rawFields, roleForbiddenCreateFields); field != "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is not valid on a Role; its model and runtime come from the tier", field))
		return
	}
	if hasField(rawFields, "implements_role_id") {
		writeError(w, http.StatusBadRequest, "a Role cannot implement another Role")
		return
	}
	if hasField(rawFields, "skill_ids") {
		writeError(w, http.StatusBadRequest, "skill_ids is not valid on a Role in this slice; skills are inherited from an implementing Agent later")
		return
	}
	if req.TierRole != "" {
		if err := validateAgentTierRole(req.TierRole); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	_, hasTargets := rawFields["invocation_targets"]
	legacyVis := req.Visibility
	perm, _, permErr := parsePermissionInput(wsUUID, req.PermissionMode, req.InvocationTargets, req.PermissionMode != nil, hasTargets, &legacyVis)
	if permErr != nil {
		writeError(w, http.StatusBadRequest, permErr.Error())
		return
	}

	avatarURL, ok := h.newAgentAvatar(w, r, req.AvatarURL)
	if !ok {
		return
	}
	sp, _ := json.Marshal(conversationStarters)

	isFirstAgent := false
	if existing, listErr := h.Queries.ListAgents(r.Context(), wsUUID); listErr == nil {
		isFirstAgent = len(existing) == 0
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start Role create transaction")
		return
	}
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:          wsUUID,
		Name:                 req.Name,
		Description:          req.Description,
		Instructions:         req.Instructions,
		AvatarUrl:            avatarURL,
		RuntimeMode:          "cloud",
		RuntimeConfig:        []byte("{}"),
		RuntimeID:            pgtype.UUID{},
		Visibility:           perm.legacyVisibility(),
		PermissionMode:       perm.mode,
		MaxConcurrentTasks:   req.MaxConcurrentTasks,
		OwnerID:              parseUUID(ownerID),
		CustomEnv:            []byte("{}"),
		CustomArgs:           []byte("[]"),
		ConversationStarters: sp,
		AccountMode:          "default",
		Subtype:              "role",
		TierRole:             pgtype.Text{String: req.TierRole, Valid: req.TierRole != ""},
	})
	if err != nil {
		_ = tx.Rollback(r.Context())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique" {
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", req.Name))
			return
		}
		slog.Warn("create role failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create Role")
		return
	}
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, created.ID, parseUUID(ownerID), perm.targets); err != nil {
		_ = tx.Rollback(r.Context())
		writeError(w, http.StatusInternalServerError, "failed to save Role access")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit Role create")
		return
	}
	slog.Info("role created", append(logger.RequestAttrs(r), "agent_id", uuidToString(created.ID), "name", created.Name, "workspace_id", workspaceID)...)

	resp := h.agentToResponse(created)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, created.ID); err != nil {
		slog.Warn("create role: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err)...)
	}
	actorType, actorID := h.resolveActor(r, ownerID, workspaceID)
	h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.AgentCreated(ownerID, workspaceID, uuidToString(created.ID), "", "", req.Template, isFirstAgent))
	redactAgentResponseForActor(&resp, actorType)
	writeJSON(w, http.StatusCreated, resp)
}
