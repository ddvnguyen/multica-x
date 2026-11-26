package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// M-499 slice 2. A Role is an agent-backed identity with no model/runtime
// configuration (M-501): at pick time the Role is authorized, then a tier is
// chosen from the Role's role-keyed tier list (M-497 model_tier_role, the data
// behind GET /tiers?role=). The existing tier resolver's eligibility, runtime,
// member-slot, cooldown and quality-floor helpers are reused unchanged; only
// the source of the candidate tier list differs from a use_tiers Agent.

// roleRunEnabled is true only for a Role row with the workspace flag on. Every
// Role-specific enqueue/restamp branch uses it so a flag-off Role-shaped row
// keeps the pre-M-510 Agent path (the M-501 spike depends on that).
func roleRunEnabled(ctx context.Context, s *TaskService, agent db.Agent) bool {
	return roleRunEnabledWithFlags(ctx, s.FeatureFlags, agent)
}

// roleRunEnabledWithFlags is the flag-only form used by the shared readiness
// predicate, which receives flags through RuntimeLookup rather than a service.
func roleRunEnabledWithFlags(ctx context.Context, flags *featureflag.Service, agent db.Agent) bool {
	return agent.Subtype == "role" &&
		featureflags.AgentRoleTypeEnabledForWorkspace(ctx, flags, util.UUIDToString(agent.WorkspaceID))
}

var (
	// errRoleUnavailable is the fail-closed outcome for a Role that is missing,
	// archived, of the wrong subtype, in the wrong workspace, or behind a flag
	// that is off. It is never masked by a fallback.
	errRoleUnavailable = errors.New("role is unavailable")
	// errRoleAccessDenied is the fail-closed outcome when the picking member
	// cannot invoke the Role under the Role's own visibility rules. A Role never
	// inherits an implementing Agent's privileges.
	errRoleAccessDenied = errors.New("role is not accessible to this member")
)

// roleTierCandidates loads the Role's auto-pickable tiers from the M-497
// role-keyed index and returns them ordered by that role's idx (then creation
// order, then id for a stable tie-break). The index is the single source of
// truth for a role's ordered tier list; agent_tier selections are deliberately
// NOT consulted for a Role. The returned idx map is the role-specific order
// key used by the quality fallback's tie-break.
func (s *TaskService) roleTierCandidates(ctx context.Context, workspaceID pgtype.UUID, roleKey string) ([]db.ModelTier, map[string]int32, error) {
	var none []db.ModelTier
	if roleKey == "" || s == nil || s.Queries == nil {
		return none, nil, nil
	}
	indexRows, err := s.Queries.ListModelTierRolesByWorkspace(ctx, workspaceID)
	if err != nil {
		return none, nil, err
	}
	idxByTier := make(map[string]int32)
	for _, row := range indexRows {
		if row.Role == roleKey {
			idxByTier[util.UUIDToString(row.TierID)] = row.Idx
		}
	}
	if len(idxByTier) == 0 {
		return none, nil, nil
	}
	tiers, err := s.Queries.ListModelTiers(ctx, workspaceID)
	if err != nil {
		return none, nil, err
	}
	candidates := make([]db.ModelTier, 0, len(idxByTier))
	for _, tier := range tiers {
		if !tier.Enabled || tier.ManualOnly || !tier.AutoPick {
			continue
		}
		if _, ok := idxByTier[util.UUIDToString(tier.ID)]; ok {
			candidates = append(candidates, tier)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ii, jj := idxByTier[util.UUIDToString(candidates[i].ID)], idxByTier[util.UUIDToString(candidates[j].ID)]
		if ii != jj {
			return ii < jj
		}
		if !candidates[i].CreatedAt.Time.Equal(candidates[j].CreatedAt.Time) {
			return candidates[i].CreatedAt.Time.Before(candidates[j].CreatedAt.Time)
		}
		return util.UUIDToString(candidates[i].ID) < util.UUIDToString(candidates[j].ID)
	})
	return candidates, idxByTier, nil
}

// roleExplicitTier validates a forced pick for a Role. The named tier must be
// enabled, live in the Role's workspace and carry a row in the Role's role-keyed
// tier list (the role filter is NOT bypassed by a deliberate forced pick: a Role
// may only run on tiers configured for its tier-role). Manual-only tiers need a
// workspace owner requester. Anything else is errUnknownTier (blocked, no
// fallback), mirroring the Agent explicit-pick contract.
func (s *TaskService) roleExplicitTier(ctx context.Context, role db.Agent, roleKey string, tierID, requester pgtype.UUID) (db.ModelTier, error) {
	var none db.ModelTier
	if !tierID.Valid {
		return none, errUnknownTier
	}
	tier, err := s.Queries.GetModelTierInWorkspace(ctx, db.GetModelTierInWorkspaceParams{
		ID: tierID, WorkspaceID: role.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, errUnknownTier
		}
		return none, err
	}
	if !tier.Enabled {
		return none, errUnknownTier
	}
	indexRows, err := s.Queries.ListModelTierRolesByWorkspace(ctx, role.WorkspaceID)
	if err != nil {
		return none, err
	}
	inRoleList := false
	for _, row := range indexRows {
		if row.Role == roleKey && util.UUIDToString(row.TierID) == util.UUIDToString(tier.ID) {
			inRoleList = true
			break
		}
	}
	if !inRoleList {
		return none, errUnknownTier
	}
	if tier.ManualOnly && !s.isWorkspaceOwner(ctx, role.WorkspaceID, requester) {
		return none, errUnknownTier
	}
	return tier, nil
}

// authorizeRoleForRun fails closed unless the Role is live, in the given
// workspace, and the picking member may invoke it under the Role's OWN rules:
// the owner, a workspace owner/admin, or a member on a public_to allow-list.
// An invalid requester authorizes nobody — a Role pick is always a deliberate
// member action. Implementing Agents are never consulted.
func (s *TaskService) authorizeRoleForRun(ctx context.Context, role db.Agent, workspaceID, requester pgtype.UUID) error {
	if role.Subtype != "role" || role.ArchivedAt.Valid ||
		util.UUIDToString(role.WorkspaceID) != util.UUIDToString(workspaceID) ||
		!featureflags.AgentRoleTypeEnabledForWorkspace(ctx, s.FeatureFlags, util.UUIDToString(role.WorkspaceID)) {
		return errRoleUnavailable
	}
	if !requester.Valid {
		return errRoleAccessDenied
	}
	if util.UUIDToString(role.OwnerID) == util.UUIDToString(requester) {
		return nil
	}
	member, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: requester, WorkspaceID: role.WorkspaceID,
	})
	if err == nil && (member.Role == "owner" || member.Role == "admin") {
		return nil
	}
	if role.PermissionMode == "public_to" {
		targets, terr := s.Queries.ListAgentInvocationTargets(ctx, role.ID)
		if terr == nil && memberHitsRoleTargets(targets, util.UUIDToString(requester)) {
			return nil
		}
	}
	return errRoleAccessDenied
}

// rolePickRequester is the human a Role pick is authorized against when the
// immediate actor is not a member (M-597). An agent actor carries no member id —
// memberActorUserID deliberately refuses to turn an agent id into an accountable
// human — so the pick is judged by the resolved top-of-chain human originator,
// exactly as invokeAgentDecision judges an agent-initiated invocation. When no
// human resolves, the requester stays invalid and authorizeRoleForRun keeps
// failing closed.
func rolePickRequester(actorUserID, originatorUserID pgtype.UUID) pgtype.UUID {
	if actorUserID.Valid {
		return actorUserID
	}
	return originatorUserID
}

// memberHitsRoleTargets is the service-side twin of the handler's invocation
// target predicate: a workspace target admits any member, a member target
// admits the matching user, team targets are inert.
func memberHitsRoleTargets(targets []db.AgentInvocationTarget, userID string) bool {
	for _, target := range targets {
		switch target.TargetType {
		case "workspace":
			return true
		case "member":
			if util.UUIDToString(target.TargetID) == userID {
				return true
			}
		}
	}
	return false
}

// resolveRoleTier auto-resolves the Role's tier using the role-keyed candidate
// list, reusing the Agent tier resolver's capacity/quality helpers verbatim.
func (s *TaskService) resolveRoleTier(ctx context.Context, role db.Agent, roleKey string, issueID pgtype.UUID, properties []byte) (tierResolution, error) {
	var none tierResolution
	candidates, idxByTier, err := s.roleTierCandidates(ctx, role.WorkspaceID, roleKey)
	if err != nil {
		return none, err
	}
	if len(candidates) == 0 {
		return none, errNoTierCapacity
	}
	important, complex := 1, 1
	if issueID.Valid {
		important, complex = s.difficultyFromProperties(ctx, role.WorkspaceID, properties)
	}
	required := requiredTierQuality(roleKey, important, complex)
	membersByTier, runtimes := s.batchTierMembersAndRuntimes(ctx, candidates)
	now := time.Now()
	if res, ok := firstTierMemberWithCapacity(ctx, s.Queries, qualifyingTiers(candidates, required), membersByTier, runtimes, now); ok {
		return tierResolutionForMember(res, false, !res.tier.Quality.Valid), nil
	}
	if res, ok := fallbackTierMember(ctx, s.Queries, candidates, membersByTier, runtimes, required, now,
		func(t db.ModelTier) int32 { return idxByTier[util.UUIDToString(t.ID)] }); ok {
		return tierResolutionForMember(res, true, false), nil
	}
	return none, errNoTierCapacity
}

// applyRoleTierResolution is the single decision point for a Role pick. It is
// used at enqueue and again on restamp, so authorization and forced-tier
// validation are rechecked whenever a queued row retries. Forced picks park on
// their own tier (no fallback); auto picks park when no candidate has capacity.
// A one-off runtime+model override or a preset cannot bypass the Role's tier.
func (s *TaskService) applyRoleTierResolution(ctx context.Context, role db.Agent, workspaceID, issueID pgtype.UUID, properties []byte, override TierTriggerOverride) (res tierResolution, parked pgtype.Bool, queuedOnly bool, err error) {
	var none tierResolution
	if err := s.authorizeRoleForRun(ctx, role, workspaceID, override.Requester); err != nil {
		return none, pgtype.Bool{}, false, err
	}
	if !role.TierRole.Valid || role.TierRole.String == "" {
		return none, pgtype.Bool{}, false, errRoleUnavailable
	}
	roleKey := normalizeTierRole(role.TierRole.String)
	if !tierRoleKeyPattern.MatchString(roleKey) {
		return none, pgtype.Bool{}, false, errRoleUnavailable
	}

	if !override.IsZero() {
		if override.RuntimeID.Valid || override.PresetID.Valid {
			// A Role has no model/runtime config and is not a preset: neither
			// override can name the run's route.
			return none, pgtype.Bool{}, false, errUnknownTier
		}
		if override.TierID.Valid {
			tier, terr := s.roleExplicitTier(ctx, role, roleKey, override.TierID, override.Requester)
			if terr == nil {
				stamp, serr := s.stampExplicitTier(ctx, tier)
				if serr == nil {
					return stamp, pgtype.Bool{}, false, nil
				}
				if !errors.Is(serr, errNoExplicitTierCapacity) {
					return none, pgtype.Bool{}, false, serr
				}
				// Park with the forced tier stamped so the row is visible and
				// claims as the Role once a member slot frees on restamp.
				return tierResolution{
					modelOverride:     pgtype.Text{String: tier.Model, Valid: tier.Model != ""},
					thinkingLevel:     tierTextOrNull(tier.ThinkingLevel),
					serviceTier:       tierTextOrNull(tier.ServiceTier),
					tierID:            tier.ID,
					qualityUnverified: !tier.Quality.Valid,
				}, pgtype.Bool{Bool: true, Valid: true}, true, nil
			}
			return none, pgtype.Bool{}, false, terr
		}
	}

	resolved, rerr := s.resolveRoleTier(ctx, role, roleKey, issueID, properties)
	if rerr == nil {
		return resolved, pgtype.Bool{}, false, nil
	}
	if errors.Is(rerr, errNoTierCapacity) {
		return none, pgtype.Bool{Bool: true, Valid: true}, true, nil
	}
	return none, pgtype.Bool{}, false, rerr
}
