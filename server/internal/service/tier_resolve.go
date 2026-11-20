package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// tierResolution is the enqueue-time stamp an agent with use_tiers writes on
// its task row (M-351). runtime_id routes the claim; the overrides ride the
// daemon claim response so the row is self-describing after a rebind.
type tierResolution struct {
	runtimeID     pgtype.UUID
	modelOverride pgtype.Text
	thinkingLevel pgtype.Text
	serviceTier   pgtype.Text
	tierID        pgtype.UUID
	hasCapacity   bool
	// M-423: floor missed (fallback stamp) / stamped tier has no measured
	// quality (unknown). Both ride the task row for the run badge.
	qualityFallback   bool
	qualityUnverified bool
}

// errNoTierCapacity signals "agent uses tiers but no selected tier has a free
// provider slot right now". Callers keep the task queued (retry on the next
// capacity change), never fail it: a full tier is backpressure, not an error.
var errNoTierCapacity = errors.New("no selected tier has capacity")

// errUnknownTier signals "explicit `@agent#tier` named a tier that does not
// exist, is disabled, or is not selected for this agent" (M-359). Callers
// block the trigger with tier_unavailable: no fallback, no queue.
var errUnknownTier = errors.New("unknown or unavailable tier")

// errNoExplicitTierCapacity signals "the explicit tier is cooling, offline or
// full" (M-359). Callers stamp the parked row and queue it anyway:
// tier_waiting_capacity is success-with-state, never a fallback.
var errNoExplicitTierCapacity = errors.New("explicit tier has no capacity")

// DefaultTierCooldown is how long a tier is skipped after its provider refused
// work with a capacity/rate-limit or quota failure (M-352). The daemon reports
// only the failure text — no Retry-After reaches the server — so the fail path
// arms this window; it is the same "short default" the Owner decision names.
const DefaultTierCooldown = 15 * time.Minute

// tierCooldownReasons are the failure reasons that mean "this provider is
// refusing right now" rather than "this task's work failed": a capacity/rate
// limit (429/529, overloaded) and an exhausted quota/billing window (402,
// usage limit). Both are already non-retryable, which is the same contract —
// do not retry on the same tier while it cools.
var tierCooldownReasons = map[string]bool{
	taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(): true,
	taskfailure.ReasonAgentProviderQuotaLimit.String():          true,
}

// tierSlotID maps a task row (or trigger override) onto the pending slot
// the M-359 guard keys on: an explicit pick occupies its tier's slot, while
// plain triggers and auto-resolved rows share the NULL slot — so a plain
// `@agent` sees (and coalesces with) queued auto runs instead of
// double-enqueueing beside them.
func tierSlotID(tierID pgtype.UUID, explicit pgtype.Bool) pgtype.UUID {
	if explicit.Bool && explicit.Valid && tierID.Valid {
		return tierID
	}
	return pgtype.UUID{}
}

// tierCoolingDown reports whether a tier's provider is inside its cooldown
// window. A NULL or expired deadline is not cooling: the tier re-enters idx
// order by itself at the first resolve after cooldown_until.
func tierCoolingDown(until pgtype.Timestamptz, now time.Time) bool {
	return until.Valid && until.Time.After(now)
}

// M-423: role bands for required quality. Lo..hi per agent role; difficulty
// (important_level/complexity 1..5, set during planning via issue Properties)
// interpolates within the band: required = lo + ceil((hi-lo)*(i+c-2)/8).
// Orchestrator follows the Leader band (Owner grouped them).
var tierQualityBands = map[string][2]int{
	"architect":    {72, 75},
	"leader":       {65, 70},
	"reviewer":     {68, 72},
	"orchestrator": {65, 70},
	"engineer":     {50, 65},
	"planning":     {65, 80},
}

// validTierRoles is the closed role vocabulary shared by agent.role and
// tier.roles (mirrors the 735 CHECK constraints).
var validTierRoles = map[string]bool{
	"architect": true, "leader": true, "reviewer": true,
	"orchestrator": true, "engineer": true, "planning": true,
}

// tierAgentRole normalizes agent.Role: set + valid → that role; anything
// else (NULL, empty, unknown) → "" meaning legacy/unfiltered.
func tierAgentRole(agent db.Agent) string {
	if !agent.Role.Valid {
		return ""
	}
	if validTierRoles[agent.Role.String] {
		return agent.Role.String
	}
	return ""
}

// difficultyFromProperties reads important_level/complexity (1..5) from the
// issue's Properties bag. Definition lookup is deterministic: active,
// workspace-scoped, number-type defs named (case-insensitively)
// important_level / complexity; on duplicates the lowest property-UUID
// (lexicographic) wins. Values parse as JSON numbers; anything missing,
// invalid, fractional-non-integral, or out of 1..5 → 1.
func (s *TaskService) difficultyFromProperties(ctx context.Context, workspaceID pgtype.UUID, properties []byte) (important, complex int) {
	important, complex = 1, 1
	if len(properties) == 0 {
		return 1, 1
	}
	defs, err := s.Queries.ListIssueProperties(ctx, db.ListIssuePropertiesParams{
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return 1, 1
	}
	wanted := map[string]*string{}
	for _, d := range defs {
		if d.ArchivedAt.Valid || d.Type != "number" {
			continue
		}
		switch lowerTierPropName(d.Name) {
		case "important_level", "complexity":
			id := util.UUIDToString(d.ID)
			if prev, ok := wanted[d.Name]; !ok || id < *prev {
				c := id
				wanted[lowerTierPropName(d.Name)] = &c
			}
		}
	}
	var bag map[string]json.RawMessage
	if err := json.Unmarshal(properties, &bag); err != nil {
		return 1, 1
	}
	if id, ok := wanted["important_level"]; ok {
		important = tierPropLevel(bag[*id])
	}
	if id, ok := wanted["complexity"]; ok {
		complex = tierPropLevel(bag[*id])
	}
	return important, complex
}

func lowerTierPropName(n string) string {
	return strings.ToLower(strings.TrimSpace(n))
}

// tierPropLevel coerces one JSON number to a 1..5 level; garbage → 1.
func tierPropLevel(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 1
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 1
	}
	if f != math.Trunc(f) || f < 1 || f > 5 {
		return 1
	}
	return int(f)
}

// requiredTierQuality interpolates the role band by difficulty. Unknown or
// legacy (empty) role → 0: no floor, everything eligible.
func requiredTierQuality(role string, important, complex int) int {
	band, ok := tierQualityBands[role]
	if !ok {
		return 0
	}
	lo, hi := band[0], band[1]
	return lo + int(math.Ceil(float64(hi-lo)*float64(important+complex-2)/8))
}

// isWorkspaceOwner reports whether userID holds the owner role in workspace.
// Manual-only tiers are owner-only (admins excluded): the Owner's rule is
// "manually selected by owner only".
func (s *TaskService) isWorkspaceOwner(ctx context.Context, workspaceID, userID pgtype.UUID) bool {
	if !userID.Valid {
		return false
	}
	m, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return false
	}
	return m.Role == "owner"
}

// resolveAgentTier picks the first usable tier for a use_tiers agent at
// enqueue (M-351, role/quality-aware in M-423). Candidates are the agent's
// selected tiers that are enabled AND role-matched, ordered by tier idx.
// Role match: agent with no role (legacy) keeps all selections; an agent
// with a role matches only tiers carrying that role — tiers with empty roles
// never match a role-set agent. Manual-only tiers never auto-resolve.
// Within survivors the required quality floor (role band + difficulty from
// the task issue's Properties) drops measured tiers below the floor and
// leaves measured-at-or-above plus unknown (NULL) quality tiers in one
// idx/creation pass — an earlier unknown tier is never skipped for a later
// measured one (free-first order, M-443) — and an unknown stamp is flagged
// unverified. When no eligible tier has capacity, the resolver falls back to
// the highest known-quality available role-matched non-manual tier below the
// floor (deterministic idx/creation tie-break) and stamps fallback; if none
// is available it returns errNoTierCapacity and the caller queues. Cooling,
// online/freshness and slot checks (M-352/M-351) apply to every candidate
// including fallback, and the claim fence re-checks at dispatch.
// A use_tiers=false agent never reaches here and keeps the legacy
// agent.RuntimeID path untouched.
func (s *TaskService) resolveAgentTier(ctx context.Context, agent db.Agent) (tierResolution, error) {
	return s.resolveAgentTierForIssue(ctx, agent, pgtype.UUID{}, nil)
}

// resolveAgentTierForIssue is resolveAgentTier with the task issue attached
// so difficulty (important_level/complexity Properties) can raise the floor.
// Empty issueID or nil properties → levels default to 1.
func (s *TaskService) resolveAgentTierForIssue(ctx context.Context, agent db.Agent, issueID pgtype.UUID, properties []byte) (tierResolution, error) {
	var none tierResolution
	role := tierAgentRole(agent)
	selections, err := s.Queries.ListAgentTiersForRole(ctx, db.ListAgentTiersForRoleParams{
		AgentID: agent.ID,
		Role:    role,
	})
	if err != nil {
		return none, err
	}
	enabled := make([]db.ModelTier, 0, len(selections))
	for _, sel := range selections {
		if !sel.Enabled || sel.ManualOnly {
			continue
		}
		enabled = append(enabled, sel)
	}
	if len(enabled) == 0 {
		return none, errNoTierCapacity
	}
	important, complex := 1, 1
	if issueID.Valid {
		important, complex = s.difficultyFromProperties(ctx, agent.WorkspaceID, properties)
	}
	required := requiredTierQuality(role, important, complex)
	runtimes := s.batchTierRuntimes(ctx, enabled)
	if res, ok := firstTierWithCapacity(ctx, s.Queries, qualifyingTiers(enabled, required), runtimes, time.Now()); ok {
		return tierResolutionFor(res, false, !res.tier.Quality.Valid), nil
	}
	if res, ok := fallbackTier(ctx, s.Queries, enabled, runtimes, required, time.Now()); ok {
		return tierResolutionFor(res, true, false), nil
	}
	return none, errNoTierCapacity
}

// tierAvailability is one role-matched candidate tier with its runtime and
// active-task count resolved for the capacity check.
type tierAvailability struct {
	tier    db.ModelTier
	runtime db.AgentRuntime
}

// tierResolutionFor builds the enqueue stamp, carrying the M-423 quality
// signal: fallback (floor missed) and unverified (NULL quality).
func tierResolutionFor(av tierAvailability, fallback, unverified bool) tierResolution {
	return tierResolution{
		runtimeID:         av.runtime.ID,
		modelOverride:     pgtype.Text{String: av.tier.Model, Valid: av.tier.Model != ""},
		thinkingLevel:     tierTextOrNull(av.tier.ThinkingLevel),
		serviceTier:       tierTextOrNull(av.tier.ServiceTier),
		tierID:            av.tier.ID,
		hasCapacity:       true,
		qualityFallback:   fallback,
		qualityUnverified: unverified,
	}
}

// qualifyingTiers keeps every role-matched tier the floor admits — measured
// at or above it, plus unknown-quality tiers — in the input's idx/creation
// order. One pass over this set is what preserves free-first ordering: a
// measured tier further down the table never jumps ahead of an earlier
// unknown one. The caller stamps unverified per chosen tier.
func qualifyingTiers(tiers []db.ModelTier, required int) []db.ModelTier {
	out := make([]db.ModelTier, 0, len(tiers))
	for _, t := range tiers {
		if t.Quality.Valid && required > 0 && int(t.Quality.Int32) < required {
			continue
		}
		out = append(out, t)
	}
	return out
}

// splitTiersByFloor partitions role-matched tiers at the required floor:
// eligible (measured quality >= floor, idx order kept), unknown (NULL
// quality), below (measured < floor, for fallback ranking). Tiers arrive in
// idx/creation order from ListAgentTiersForRole and each partition keeps it.
func splitTiersByFloor(tiers []db.ModelTier, required int) (eligible, unknown, below []db.ModelTier) {
	for _, t := range tiers {
		if !t.Quality.Valid {
			unknown = append(unknown, t)
			continue
		}
		if required > 0 && int(t.Quality.Int32) < required {
			below = append(below, t)
			continue
		}
		eligible = append(eligible, t)
	}
	return eligible, unknown, below
}

// batchTierRuntimes fetches the distinct runtimes behind candidate tiers once.
func (s *TaskService) batchTierRuntimes(ctx context.Context, tiers []db.ModelTier) map[string]db.AgentRuntime {
	runtimes := map[string]db.AgentRuntime{}
	ids := make([]pgtype.UUID, 0, len(tiers))
	for _, t := range tiers {
		if t.RuntimeID.Valid {
			ids = append(ids, t.RuntimeID)
		}
	}
	if len(ids) == 0 {
		return runtimes
	}
	rows, err := s.Queries.GetAgentRuntimes(ctx, ids)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return runtimes
	}
	for _, rt := range rows {
		runtimes[util.UUIDToString(rt.ID)] = rt
	}
	return runtimes
}

// firstTierWithCapacity returns the first tier (idx/creation order) that is
// not cooling, bound to an online/fresh runtime, and below provider_slots.
// The caller threads the same now through the whole pass so a tier cooling
// at pass start is judged consistently.
func firstTierWithCapacity(ctx context.Context, q *db.Queries, tiers []db.ModelTier, runtimes map[string]db.AgentRuntime, now time.Time) (tierAvailability, bool) {
	for _, t := range tiers {
		av, ok := tierCapacity(ctx, q, t, runtimes, now)
		if ok {
			return av, true
		}
	}
	return tierAvailability{}, false
}

// tierCapacity applies the M-351/M-352 availability chain to one tier.
func tierCapacity(ctx context.Context, q *db.Queries, t db.ModelTier, runtimes map[string]db.AgentRuntime, now time.Time) (tierAvailability, bool) {
	if tierCoolingDown(t.CooldownUntil, now) {
		return tierAvailability{}, false
	}
	rt, ok := runtimes[util.UUIDToString(t.RuntimeID)]
	if !t.RuntimeID.Valid || !ok {
		return tierAvailability{}, false
	}
	if !isRuntimeClaimable(rt) {
		return tierAvailability{}, false
	}
	running, err := q.CountActiveTasksByRuntime(ctx, rt.ID)
	if err != nil {
		return tierAvailability{}, false
	}
	slots := int64(t.ProviderSlots)
	if slots < 1 {
		slots = 1
	}
	if running >= slots {
		return tierAvailability{}, false
	}
	return tierAvailability{tier: t, runtime: rt}, true
}

// fallbackTier picks the highest known-quality available tier below the
// floor when nothing eligible has capacity. Deterministic: quality desc,
// then idx asc, then created_at asc. Unknown-quality tiers never serve as
// fallback (they ran in the eligible pass, flagged unverified); manual-only
// tiers never serve as fallback. Returns false when none is available, and
// the caller queues (errNoTierCapacity).
func fallbackTier(ctx context.Context, q *db.Queries, tiers []db.ModelTier, runtimes map[string]db.AgentRuntime, required int, now time.Time) (tierAvailability, bool) {
	_, _, below := splitTiersByFloor(tiers, required)
	if len(below) == 0 {
		return tierAvailability{}, false
	}
	ordered := append([]db.ModelTier(nil), below...)
	sortTiersByQualityDesc(ordered)
	for _, t := range ordered {
		if av, ok := tierCapacity(ctx, q, t, runtimes, now); ok {
			return av, true
		}
	}
	return tierAvailability{}, false
}

// sortTiersByQualityDesc orders measured tiers quality-desc, idx-asc,
// created_at-asc. Callers only pass measured tiers (no NULL quality).
func sortTiersByQualityDesc(tiers []db.ModelTier) {
	sort.SliceStable(tiers, func(i, j int) bool {
		qi, qj := tiers[i].Quality.Int32, tiers[j].Quality.Int32
		if qi != qj {
			return qi > qj
		}
		if tiers[i].Idx != tiers[j].Idx {
			return tiers[i].Idx < tiers[j].Idx
		}
		return tiers[i].CreatedAt.Time.Before(tiers[j].CreatedAt.Time)
	})
}

// isRuntimeClaimable mirrors the online/freshness half of the ClaimAgentTask
// fence: status online and a heartbeat within RuntimeClaimFreshnessSeconds.
// Owner/visibility checks stay in the claim path; enqueue only avoids
// stamping a row that no daemon could pick up yet.
func isRuntimeClaimable(rt db.AgentRuntime) bool {
	if rt.Status != "online" {
		return false
	}
	lastSeen := rt.UpdatedAt.Time
	if rt.LastSeenAt.Valid {
		lastSeen = rt.LastSeenAt.Time
	}
	if lastSeen.IsZero() {
		return false
	}
	return time.Since(lastSeen).Seconds() <= RuntimeClaimFreshnessSeconds
}

// tierTextOrNull maps a nullable tier text column onto pgtype.Text.
func tierTextOrNull(v pgtype.Text) pgtype.Text {
	if !v.Valid || v.String == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: v.String, Valid: true}
}

// resolveExplicitTier stamps one named tier for an `@agent#tier` trigger
// (M-359, owner-gated in M-423). The tier must exist in the agent's
// workspace, be enabled, and — when the agent uses tiers — be among the
// agent's selections. Manual-only tiers additionally require the requester
// to be a workspace owner; anyone else gets errUnknownTier (blocked, no
// fallback, no information leak). A use_tiers=false agent may name any
// enabled workspace tier: the pick overrides the single runtime for that run.
// Missing/disabled/unselected/non-owner-manual → errUnknownTier (blocked).
// Cooling, offline or full → errNoExplicitTierCapacity (parked, no fallback).
func (s *TaskService) resolveExplicitTier(ctx context.Context, agent db.Agent, workspaceID pgtype.UUID, tierName string) (tierResolution, error) {
	return s.resolveExplicitTierAs(ctx, agent, workspaceID, tierName, pgtype.UUID{})
}

// resolveExplicitTierAs is resolveExplicitTier with the triggering
// requester's user id for the manual-only owner check. Callers that know who
// triggered (comment mentions, assign writes, chat tier picks, quick-create)
// MUST pass it; an invalid (unknown) requester never satisfies manual-only.
func (s *TaskService) resolveExplicitTierAs(ctx context.Context, agent db.Agent, workspaceID pgtype.UUID, tierName string, requester pgtype.UUID) (tierResolution, error) {
	var none tierResolution
	tier, err := s.LookupExplicitTierAs(ctx, agent, workspaceID, tierName, requester)
	if err != nil {
		return none, err
	}
	return s.stampExplicitTier(ctx, tier)
}

// resolveExplicitTierByID is resolveExplicitTier for an already-validated
// tier UUID (tier picker, API override). Same semantics: unknown/disabled/
// unselected/non-owner-manual → errUnknownTier, cooling/offline/full →
// errNoExplicitTierCapacity.
func (s *TaskService) resolveExplicitTierByID(ctx context.Context, agent db.Agent, tierID pgtype.UUID) (tierResolution, error) {
	return s.resolveExplicitTierByIDAs(ctx, agent, tierID, pgtype.UUID{})
}

// resolveExplicitTierByIDAs is resolveExplicitTierByID with the triggering
// requester's user id for the manual-only owner check. The parked-explicit
// restamp path passes the task's stored originator (trusted at restamp: the
// stamp was owner-authorized at enqueue, and membership is re-checked below);
// any other invalid requester fails manual-only tiers.
func (s *TaskService) resolveExplicitTierByIDAs(ctx context.Context, agent db.Agent, tierID pgtype.UUID, requester pgtype.UUID) (tierResolution, error) {
	var none tierResolution
	tier, err := s.Queries.GetModelTierInWorkspace(ctx, db.GetModelTierInWorkspaceParams{
		ID:          tierID,
		WorkspaceID: agent.WorkspaceID,
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
	if tier.ManualOnly && !s.isWorkspaceOwner(ctx, agent.WorkspaceID, requester) {
		return none, errUnknownTier
	}
	if agent.UseTiers {
		return s.resolveExplicitTierAs(ctx, agent, agent.WorkspaceID, tier.Name, requester)
	}
	return s.stampExplicitTier(ctx, tier)
}

// LookupExplicitTier validates one named tier for an explicit pick (M-359)
// and returns its row: the tier must exist in the workspace, be enabled, and
// — for a use_tiers agent — be among the agent's selections. Anything else is
// errUnknownTier (blocked, no fallback). Case-insensitive exact match: the
// (workspace_id, lower(name)) unique index admits at most one row.
// Manual-only tiers are validated here too: the unknown-requester form never
// satisfies them (use LookupExplicitTierAs with the requester instead).
func (s *TaskService) LookupExplicitTier(ctx context.Context, agent db.Agent, workspaceID pgtype.UUID, tierName string) (db.ModelTier, error) {
	return s.LookupExplicitTierAs(ctx, agent, workspaceID, tierName, pgtype.UUID{})
}

// LookupExplicitTierAs is LookupExplicitTier with the triggering requester's
// user id. Manual-only tiers require a workspace owner requester; every other
// validation is identical. Role mismatch is NOT checked on the explicit path:
// the pick narrows to a named tier (M-359) and the owner/assignee chose it
// deliberately, so role filtering applies to auto-resolve only.
func (s *TaskService) LookupExplicitTierAs(ctx context.Context, agent db.Agent, workspaceID pgtype.UUID, tierName string, requester pgtype.UUID) (db.ModelTier, error) {
	var none db.ModelTier
	tier, err := s.Queries.GetModelTierByNameInWorkspace(ctx, db.GetModelTierByNameInWorkspaceParams{
		WorkspaceID: workspaceID,
		Lower:       tierName,
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
	if tier.ManualOnly && !s.isWorkspaceOwner(ctx, workspaceID, requester) {
		return none, errUnknownTier
	}
	if agent.UseTiers {
		selected, err := s.Queries.ListAgentTierIDs(ctx, agent.ID)
		if err != nil {
			return none, err
		}
		for _, id := range selected {
			if util.UUIDToString(id) == util.UUIDToString(tier.ID) {
				return tier, nil
			}
		}
		return none, errUnknownTier
	}
	return tier, nil
}

// stampExplicitTier applies the capacity gate to one validated tier row. The
// stamp carries the M-423 quality signal: an explicit pick of an unmeasured
// (NULL quality) tier stamps unverified — never described as meeting a floor.
func (s *TaskService) stampExplicitTier(ctx context.Context, tier db.ModelTier) (tierResolution, error) {
	var none tierResolution
	if tierCoolingDown(tier.CooldownUntil, time.Now()) {
		return none, errNoExplicitTierCapacity
	}
	if !tier.RuntimeID.Valid {
		return none, errNoExplicitTierCapacity
	}
	rt, err := s.Queries.GetAgentRuntime(ctx, tier.RuntimeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, errNoExplicitTierCapacity
		}
		return none, err
	}
	if !isRuntimeClaimable(rt) {
		return none, errNoExplicitTierCapacity
	}
	running, err := s.Queries.CountActiveTasksByRuntime(ctx, rt.ID)
	if err != nil {
		return none, err
	}
	slots := int64(tier.ProviderSlots)
	if slots < 1 {
		slots = 1
	}
	if running >= slots {
		return none, errNoExplicitTierCapacity
	}
	return tierResolution{
		runtimeID:         rt.ID,
		modelOverride:     pgtype.Text{String: tier.Model, Valid: tier.Model != ""},
		thinkingLevel:     tierTextOrNull(tier.ThinkingLevel),
		serviceTier:       tierTextOrNull(tier.ServiceTier),
		tierID:            tier.ID,
		hasCapacity:       true,
		qualityUnverified: !tier.Quality.Valid,
	}, nil
}
