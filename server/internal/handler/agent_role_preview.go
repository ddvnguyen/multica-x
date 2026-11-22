package handler

import (
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RoleCompositionPreview is the read-only inherited view for the Team UI
// (M-499 slice 4). It returns exactly what a claim of this Agent would send:
// Role instructions first, the Agent's own additions after, and the union of
// both agents' enabled skills. It never mutates anything and never returns a
// Role the caller cannot read.
type RoleCompositionPreview struct {
	AgentID           string             `json:"agent_id"`
	RoleID            *string            `json:"role_id,omitempty"`
	RoleName          string             `json:"role_name,omitempty"`
	Instructions      string             `json:"instructions"`
	RoleInstructions  string             `json:"role_instructions,omitempty"`
	AgentInstructions string             `json:"agent_instructions"`
	Skills            []RolePreviewSkill `json:"skills"`
}

// RolePreviewSkill is one skill in the inherited preview. Source is "role" for
// a skill the Agent inherits from its Role, "agent" for one it owns itself; an
// ID present on both is reported once with source "role".
type RolePreviewSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

// PreviewRoleComposition serves GET /api/agents/{id}/role-preview.
//
// Authorization is the intersection of the Agent gate and the Role gate: the
// caller must be able to see the Agent exactly as GetAgent requires, and, if
// it implements a Role, must also be able to read that Role under the Role's
// own visibility rules. A caller who can see the Agent but not its Role gets a
// 403 with no Role instructions or skill names — no partial inherited content
// ever leaks. The route is human-actor-only so a task token cannot use it to
// read a Role outside its run.
func (h *Handler) PreviewRoleComposition(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := util.UUIDToString(agent.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}
	if !featureflags.AgentRoleTypeEnabledForWorkspace(r.Context(), h.FeatureFlags, workspaceID) {
		writeError(w, http.StatusForbidden, "agent role type is not enabled for this workspace")
		return
	}

	preview := RoleCompositionPreview{
		AgentID:           util.UUIDToString(agent.ID),
		AgentInstructions: agent.Instructions,
		Instructions:      agent.Instructions,
		Skills:            []RolePreviewSkill{},
	}
	composition := service.RoleComposition{}
	if agent.ImplementsRoleID.Valid {
		role, err := h.Queries.GetAgent(r.Context(), agent.ImplementsRoleID)
		if err != nil || role.Subtype != "role" || role.ArchivedAt.Valid ||
			util.UUIDToString(role.WorkspaceID) != workspaceID {
			writeError(w, http.StatusConflict, "implemented role is unavailable")
			return
		}
		// The Role is a separate identity with its own visibility rules: an
		// implementing Agent never confers read access to its Role.
		if !h.canAccessPrivateAgent(r.Context(), role, actorType, actorID, workspaceID) {
			writeError(w, http.StatusForbidden, "you do not have access to this role")
			return
		}
		composition = service.RoleComposition{
			RoleID:           role.ID,
			RoleName:         role.Name,
			RoleInstructions: role.Instructions,
			SkillAgentIDs:    []pgtype.UUID{agent.ID, role.ID},
		}
		preview.RoleID = uuidToPtr(role.ID)
		preview.RoleName = role.Name
		preview.RoleInstructions = role.Instructions
	}
	preview.Instructions = composition.InstructionsFor(agent.Instructions)

	// ListAgentSkills' junction predicate is the authorization seam for both
	// sides of the union: only enabled skills already attached to an agent the
	// caller may read are returned. Full SKILL.md bodies are deliberately not
	// loaded — this is a preview.
	agentSkills, err := h.Queries.ListAgentSkills(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	var roleSkills []db.Skill
	if composition.Active() {
		roleSkills, err = h.Queries.ListAgentSkills(r.Context(), composition.RoleID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load role skills")
			return
		}
	}
	roleSkillIDs := make(map[string]struct{}, len(roleSkills))
	for _, skill := range roleSkills {
		roleSkillIDs[util.UUIDToString(skill.ID)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(agentSkills)+len(roleSkills))
	for _, skill := range append(agentSkills, roleSkills...) {
		id := util.UUIDToString(skill.ID)
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		source := "agent"
		if _, ok := roleSkillIDs[id]; ok {
			source = "role"
		}
		preview.Skills = append(preview.Skills, RolePreviewSkill{
			ID:          id,
			Name:        skill.Name,
			Description: skill.Description,
			Source:      source,
		})
	}
	sort.Slice(preview.Skills, func(i, j int) bool {
		if preview.Skills[i].Name != preview.Skills[j].Name {
			return preview.Skills[i].Name < preview.Skills[j].Name
		}
		return preview.Skills[i].ID < preview.Skills[j].ID
	})
	writeJSON(w, http.StatusOK, preview)
}
