package handler

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/featureflags"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RoleRouteMemberResponse is one tier member's claimable state as consumed by
// the orch-sweep `role_route_ready` probe (M-588 B1d/G1, moct#55). Unknown or
// unbound runtimes report runtime_status "unknown" with no last-seen time and
// zero free slots so the probe fails closed on them.
type RoleRouteMemberResponse struct {
	RuntimeID         *string `json:"runtime_id"`
	RuntimeStatus     string  `json:"runtime_status"`
	RuntimeLastSeenAt *string `json:"runtime_last_seen_at"`
	FreeSlots         int64   `json:"free_slots"`
}

// RoleRouteTierResponse is one role-keyed tier. Disabled, manual-only and
// cooling tiers are included verbatim — eligibility is the probe's decision,
// not this endpoint's — so the sweep can tell "no eligible tier" apart from
// "no online runtime" and "no capacity".
type RoleRouteTierResponse struct {
	Name          string                    `json:"name"`
	Enabled       bool                      `json:"enabled"`
	ManualOnly    bool                      `json:"manual_only"`
	CooldownUntil *string                   `json:"cooldown_until"`
	Members       []RoleRouteMemberResponse `json:"members"`
}

// RoleRouteResponse is the read-only route snapshot for one Role. The Role's
// own status/runtime_bound are deliberately absent: a Role is offline and
// unbound by construction and still routable through its tier members.
type RoleRouteResponse struct {
	FlagOn bool                    `json:"flag_on"`
	Tiers  []RoleRouteTierResponse `json:"tiers"`
}

// GetAgentRoleRoute serves GET /api/agents/{id}/role-route.
//
// Read-only: the workspace Role-routing flag plus the Role's role-keyed tier
// list (model_tier_role) with each tier's members, member runtime state and
// free slots (provider slots minus active runs). Workspace member auth only,
// no writes. Unknown agents, non-Role agents and archived Roles are 404 so
// the CLI fails non-zero; a Role with no tier_role key reports flag_on with
// an empty tier list (fail-closed downstream, not an error here).
func (h *Handler) GetAgentRoleRoute(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if agent.Subtype != "role" || agent.ArchivedAt.Valid {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	workspaceID := uuidToString(agent.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this role")
		return
	}

	ctx := r.Context()
	resp := RoleRouteResponse{
		FlagOn: featureflags.AgentRoleTypeEnabledForWorkspace(ctx, h.FeatureFlags, workspaceID),
		Tiers:  []RoleRouteTierResponse{},
	}
	roleKey := ""
	if agent.TierRole.Valid {
		roleKey = canonicalTierRole(agent.TierRole.String)
		if !validTierRoleKey(roleKey) {
			roleKey = ""
		}
	}
	if roleKey == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	indexRows, err := h.Queries.ListModelTierRolesByWorkspace(ctx, agent.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load role tier list")
		return
	}
	idxByTier := make(map[string]int32, len(indexRows))
	for _, row := range indexRows {
		if row.Role == roleKey {
			idxByTier[uuidToString(row.TierID)] = row.Idx
		}
	}
	if len(idxByTier) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	tiers, err := h.Queries.ListModelTiers(ctx, agent.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tiers")
		return
	}
	candidates := make([]db.ModelTier, 0, len(idxByTier))
	for _, tier := range tiers {
		if _, ok := idxByTier[uuidToString(tier.ID)]; ok {
			candidates = append(candidates, tier)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ii, jj := idxByTier[uuidToString(candidates[i].ID)], idxByTier[uuidToString(candidates[j].ID)]
		if ii != jj {
			return ii < jj
		}
		if !candidates[i].CreatedAt.Time.Equal(candidates[j].CreatedAt.Time) {
			return candidates[i].CreatedAt.Time.Before(candidates[j].CreatedAt.Time)
		}
		return uuidToString(candidates[i].ID) < uuidToString(candidates[j].ID)
	})

	membersByTier := make(map[string][]db.ModelTierMember, len(candidates))
	runtimeIDs := make(map[string]pgtype.UUID)
	for _, tier := range candidates {
		tid := uuidToString(tier.ID)
		members, err := h.Queries.ListEnabledModelTierMembersByTier(ctx, tier.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load tier members")
			return
		}
		if len(members) == 0 && tier.RuntimeID.Valid {
			// Legacy tier without members: the tier-level runtime carries
			// capacity exactly as the resolver's tierCapacity fallback.
			members = []db.ModelTierMember{{
				TierID:        tier.ID,
				RuntimeID:     tier.RuntimeID,
				ProviderSlots: tier.ProviderSlots,
				CooldownUntil: tier.CooldownUntil,
				Enabled:       true,
			}}
		}
		membersByTier[tid] = members
		for _, m := range members {
			if m.RuntimeID.Valid {
				runtimeIDs[uuidToString(m.RuntimeID)] = m.RuntimeID
			}
		}
	}
	runtimes := map[string]db.AgentRuntime{}
	if len(runtimeIDs) > 0 {
		ids := make([]pgtype.UUID, 0, len(runtimeIDs))
		for _, id := range runtimeIDs {
			ids = append(ids, id)
		}
		rows, err := h.Queries.GetAgentRuntimes(ctx, ids)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load runtimes")
			return
		}
		for _, rt := range rows {
			runtimes[uuidToString(rt.ID)] = rt
		}
	}

	for _, tier := range candidates {
		out := RoleRouteTierResponse{
			Name:       tier.Name,
			Enabled:    tier.Enabled,
			ManualOnly: tier.ManualOnly,
			Members:    []RoleRouteMemberResponse{},
		}
		if tier.CooldownUntil.Valid {
			s := tier.CooldownUntil.Time.UTC().Format(time.RFC3339)
			out.CooldownUntil = &s
		}
		for _, m := range membersByTier[uuidToString(tier.ID)] {
			member, err := h.roleRouteMember(ctx, m, runtimes)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load runtime capacity")
				return
			}
			out.Members = append(out.Members, member)
		}
		resp.Tiers = append(resp.Tiers, out)
	}
	writeJSON(w, http.StatusOK, resp)
}

// roleRouteMember renders one member's claimable state. Free slots are the
// member's provider slots (at least one) minus active runs on its runtime; a
// member with no resolvable runtime reports "unknown" with zero free slots.
// A capacity-count error is returned so the caller fails closed with a 500.
func (h *Handler) roleRouteMember(ctx context.Context, m db.ModelTierMember, runtimes map[string]db.AgentRuntime) (RoleRouteMemberResponse, error) {
	out := RoleRouteMemberResponse{RuntimeStatus: "unknown"}
	if m.RuntimeID.Valid {
		s := uuidToString(m.RuntimeID)
		out.RuntimeID = &s
		if rt, ok := runtimes[s]; ok {
			out.RuntimeStatus = rt.Status
			seen := rt.UpdatedAt
			if rt.LastSeenAt.Valid {
				seen = rt.LastSeenAt
			}
			if seen.Valid {
				s := seen.Time.UTC().Format(time.RFC3339)
				out.RuntimeLastSeenAt = &s
			}
			slots := int64(m.ProviderSlots)
			if slots < 1 {
				slots = 1
			}
			running, err := h.Queries.CountActiveTasksByRuntime(ctx, rt.ID)
			if err != nil {
				return RoleRouteMemberResponse{}, err
			}
			if free := slots - running; free > 0 {
				out.FreeSlots = free
			}
		}
	}
	return out, nil
}
