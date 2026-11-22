package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
)

// M-499 slice 3. An Agent that implements a Role inherits the Role's
// instructions and enabled skills at claim time. The Role's instructions come
// FIRST and the Agent's own additions come last, so on conflict the Agent wins
// (Owner decision recorded on M-499). Skills are the union of both agents'
// enabled skill rows, deduplicated by skill id and ordered deterministically.
//
// A Role is an agent-backed identity, so a Role run already loads its own row
// through task.AgentID (slice 2). Composition is therefore only for the
// `subtype='agent' AND implements_role_id IS NOT NULL` case, and is gated by the
// same workspace flag as every other Role surface. A plain Agent never enters
// these helpers: its claim payload stays byte-for-byte unchanged.

var (
	// errRoleCompositionUnavailable is the fail-closed outcome for a Role that
	// is deleted, archived, of the wrong subtype, in another workspace, behind
	// a flag that is off, or unreadable at claim. It is never masked by a
	// shorter, Role-less payload.
	errRoleCompositionUnavailable = errors.New("implemented role is unavailable")
	// errRoleCompositionAccessDenied is the fail-closed outcome when the
	// Agent's owner may not invoke the Role under the Role's own rules. The
	// Agent never inherits a Role's privileges, and the Role never inherits an
	// Agent's, so the check is against the owner of the implementing Agent.
	errRoleCompositionAccessDenied = errors.New("agent owner cannot access the implemented role")
)

// RoleComposition is the inherited content a run of an Agent receives from
// the Role it implements. The zero value means "no composition": an ordinary
// Agent, a Role run, a flag-off workspace, or an Agent with no Role link.
type RoleComposition struct {
	RoleID           pgtype.UUID
	RoleName         string
	RoleInstructions string
	// SkillAgentIDs lists the agents whose enabled skills the run inherits,
	// in claim order: the run Agent first, then the Role. It is only used when
	// RoleID.Valid; a no-composition run keeps the single-agent load path.
	SkillAgentIDs []pgtype.UUID
}

// Active reports whether this run inherits a Role's content.
func (c RoleComposition) Active() bool { return c.RoleID.Valid }

// InstructionsFor composes the Role's instructions with the Agent's own. Role
// content comes first so the Agent's later additions take priority on conflict.
// Empty pieces are omitted without changing the bytes of the non-empty one.
func (c RoleComposition) InstructionsFor(agentInstructions string) string {
	if !c.Active() || strings.TrimSpace(c.RoleInstructions) == "" {
		return agentInstructions
	}
	if strings.TrimSpace(agentInstructions) == "" {
		return c.RoleInstructions
	}
	return c.RoleInstructions + "\n\n" + agentInstructions
}

// ResolveRoleComposition resolves the Role an Agent implements. It is called
// once per claim (and once per skill-bundle resolve) and its result drives
// both the instruction and the skill composition so the two cannot drift.
//
// Any read or validation failure is returned: the caller MUST fail the task
// closed rather than dispatch a Role-less payload that looks like a valid one.
func (s *TaskService) ResolveRoleComposition(ctx context.Context, agent db.Agent) (RoleComposition, error) {
	var none RoleComposition
	if !featureflags.AgentRoleTypeEnabledForWorkspace(ctx, s.FeatureFlags, util.UUIDToString(agent.WorkspaceID)) ||
		agent.Subtype != "agent" || !agent.ImplementsRoleID.Valid {
		return none, nil
	}
	if s.Queries == nil {
		return none, fmt.Errorf("%w: skill store unavailable", errRoleCompositionUnavailable)
	}
	role, err := s.Queries.GetAgent(ctx, agent.ImplementsRoleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, fmt.Errorf("%w: role %s was deleted", errRoleCompositionUnavailable, util.UUIDToString(agent.ImplementsRoleID))
		}
		return none, fmt.Errorf("%w: read role %s: %v", errRoleCompositionUnavailable, util.UUIDToString(agent.ImplementsRoleID), err)
	}
	// authorizeRoleForRun checks subtype, archived, workspace, flag and the
	// Role's own visibility rules. Reusing it (with the implementing Agent's
	// owner as requester) keeps claim-time authorization identical to the
	// pick-time authorization in slice 2.
	if err := s.authorizeRoleForRun(ctx, role, agent.WorkspaceID, agent.OwnerID); err != nil {
		if errors.Is(err, errRoleAccessDenied) {
			return none, fmt.Errorf("%w: %v", errRoleCompositionAccessDenied, err)
		}
		return none, fmt.Errorf("%w: %v", errRoleCompositionUnavailable, err)
	}
	return RoleComposition{
		RoleID:           role.ID,
		RoleName:         role.Name,
		RoleInstructions: role.Instructions,
		SkillAgentIDs:    []pgtype.UUID{agent.ID, role.ID},
	}, nil
}

// LoadComposedSkills loads the enabled skills a run inherits. The inactive
// path delegates to LoadAgentSkills so a no-Role Agent is byte-for-byte the
// pre-slice-3 result (including its existing name ordering).
func (s *TaskService) LoadComposedSkills(ctx context.Context, comp RoleComposition, agent db.Agent) ([]AgentSkillData, error) {
	if !comp.Active() {
		return s.LoadAgentSkills(ctx, agent.ID)
	}
	var combined []db.Skill
	seen := make(map[string]struct{})
	for _, agentID := range comp.SkillAgentIDs {
		skills, err := s.Queries.ListAgentSkills(ctx, agentID)
		if err != nil {
			return nil, fmt.Errorf("list inherited agent skills: %w", err)
		}
		for _, skill := range skills {
			id := util.UUIDToString(skill.ID)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			combined = append(combined, skill)
		}
	}
	// ListAgentSkills already orders by name; the id tie-break makes the union
	// deterministic even when two skills share a name.
	sort.SliceStable(combined, func(i, j int) bool {
		if combined[i].Name != combined[j].Name {
			return combined[i].Name < combined[j].Name
		}
		return util.UUIDToString(combined[i].ID) < util.UUIDToString(combined[j].ID)
	})
	return s.skillsWithFiles(ctx, combined)
}

// LoadComposedSkillBundles is the skill-ref claim path: the same union as
// LoadComposedSkills, with stable bundle hashes and daemon-facing refs.
func (s *TaskService) LoadComposedSkillBundles(ctx context.Context, comp RoleComposition, agent db.Agent, agentSystemKey string, legacyRedirects bool) ([]AgentSkillData, []AgentSkillRefData, error) {
	skills, err := s.LoadComposedSkills(ctx, comp, agent)
	if err != nil {
		return nil, nil, err
	}
	skills = append(skills, s.BuiltinSkills(agentSystemKey, legacyRedirects)...)
	bundles, refs := BuildAgentSkillBundles(skills)
	return bundles, refs, nil
}

// LoadRequestedComposedSkillBundles is the resolve path used after claim.
// The daemon resolves one ref at a time against task.AgentID, so a Role skill
// that was advertised in the claim refs must also resolve from the Role's
// junction here — otherwise the run starts with a dangling ref.
func (s *TaskService) LoadRequestedComposedSkillBundles(ctx context.Context, comp RoleComposition, agent db.Agent, refs []AgentSkillBundleRef) (map[string]AgentSkillData, error) {
	if !comp.Active() {
		return s.LoadRequestedAgentSkillBundles(ctx, agent.ID, refs)
	}
	requestedIDs := make([]pgtype.UUID, 0, len(refs))
	seenWorkspace := make(map[string]struct{}, len(refs))
	wantBuiltin := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		switch ref.Source {
		case skillbundle.SourceBuiltin:
			wantBuiltin[ref.ID] = struct{}{}
		case skillbundle.SourceWorkspace:
			if _, ok := seenWorkspace[ref.ID]; ok {
				continue
			}
			id, err := util.ParseUUID(ref.ID)
			if err != nil {
				// An unparseable id matches no row, which is the same as an id
				// the agent does not have; skip without failing the other refs.
				continue
			}
			seenWorkspace[ref.ID] = struct{}{}
			requestedIDs = append(requestedIDs, id)
		}
	}

	var requested []AgentSkillData
	seenSkill := make(map[string]struct{})
	if len(requestedIDs) > 0 {
		var dbSkills []db.Skill
		for _, agentID := range comp.SkillAgentIDs {
			skills, err := s.Queries.ListAgentSkillsByIDs(ctx, db.ListAgentSkillsByIDsParams{
				AgentID:  agentID,
				SkillIds: requestedIDs,
			})
			if err != nil {
				return nil, fmt.Errorf("list inherited agent skills by ids: %w", err)
			}
			for _, skill := range skills {
				id := util.UUIDToString(skill.ID)
				if _, ok := seenSkill[id]; ok {
					continue
				}
				seenSkill[id] = struct{}{}
				dbSkills = append(dbSkills, skill)
			}
		}
		if len(dbSkills) > 0 {
			// Fail closed on a partial file read for the same reason as
			// LoadAgentSkills: a short bundle hashes and validates like a
			// complete one, so nothing downstream can detect it.
			loaded, err := s.skillsWithFiles(ctx, dbSkills)
			if err != nil {
				return nil, err
			}
			requested = append(requested, loaded...)
		}
	}
	if len(wantBuiltin) > 0 {
		for _, builtin := range s.AllBuiltinSkills() {
			if _, ok := wantBuiltin[BuiltinSkillID(builtin.Name)]; ok {
				requested = append(requested, builtin)
			}
		}
	}

	bundles, _ := BuildAgentSkillBundles(requested)
	resolved := make(map[string]AgentSkillData, len(bundles))
	for _, bundle := range bundles {
		resolved[AgentSkillBundleKey(bundle.Source, bundle.ID)] = bundle
	}
	return resolved, nil
}
