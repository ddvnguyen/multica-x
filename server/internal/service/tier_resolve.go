package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
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
	memberID      pgtype.UUID
	hasCapacity   bool
	// M-423: floor missed (fallback stamp) / stamped tier has no measured
	// quality (unknown). Both ride the task row for the run badge.
	qualityFallback      bool
	qualityUnverified    bool
	presetFallbackReason string
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

// M-497 (Owner Q4): roles are free workspace-defined keys. The three
// classic roles keep their quality bands; legacy names fold onto them during
// the transition. Custom keys pass through and resolve with no floor
// (requiredTierQuality returns 0 for unknown bands).
var tierQualityBands = map[string][2]int{
	"architect":  {72, 75},
	"management": {65, 72},
	"engineer":   {50, 65},
	// Legacy
	"leader":       {65, 72},
	"reviewer":     {65, 72},
	"orchestrator": {65, 72},
	"planning":     {72, 75},
}

// tierRoleKeyPattern constrains role keys to slug shape (mirrors the
// tier_role registry CHECK and handler validation).
var tierRoleKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func normalizeTierRole(r string) string {
	switch r {
	case "leader", "reviewer", "orchestrator":
		return "management"
	case "planning":
		return "architect"
	default:
		return r
	}
}

// tierAgentRole normalizes agent.Role; legacy roles map to the classic
// bands, custom workspace-defined keys pass through. Empty or malformed
// roles mean legacy unfiltered resolution.
func tierAgentRole(agent db.Agent) string {
	if !agent.Role.Valid {
		return ""
	}
	r := normalizeTierRole(agent.Role.String)
	if !tierRoleKeyPattern.MatchString(r) {
		return ""
	}
	return r
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
	roleConfig := s.tierRoleConfig(ctx, selections, role)
	enabled := make([]db.ModelTier, 0, len(selections))
	for _, sel := range selections {
		if !sel.Enabled || sel.ManualOnly {
			continue
		}
		if role == "" {
			if !sel.AutoPick {
				continue
			}
		} else if setting, ok := roleConfig[util.UUIDToString(sel.ID)]; !ok || !setting.autoPick {
			continue
		}
		enabled = append(enabled, sel)
	}
	if len(enabled) == 0 {
		return none, errNoTierCapacity
	}
	// M-497: order candidates by the role-specific idx for the agent's
	// role (then creation order). Tiers without a row for the role keep
	// their global idx — legacy tiers with no role match only legacy
	// agents, for whom no rows exist.
	idxOf := func(t db.ModelTier) int32 { return effectiveTierIdx(t, roleConfig) }
	sort.SliceStable(enabled, func(i, j int) bool {
		ii, jj := idxOf(enabled[i]), idxOf(enabled[j])
		if ii != jj {
			return ii < jj
		}
		return enabled[i].CreatedAt.Time.Before(enabled[j].CreatedAt.Time)
	})
	important, complex := 1, 1
	if issueID.Valid {
		important, complex = s.difficultyFromProperties(ctx, agent.WorkspaceID, properties)
	}
	required := requiredTierQuality(role, important, complex)
	membersByTier, runtimes := s.batchTierMembersAndRuntimes(ctx, enabled)
	now := time.Now()
	if res, ok := firstTierMemberWithCapacity(ctx, s.Queries, qualifyingTiers(enabled, required), membersByTier, runtimes, now); ok {
		return tierResolutionForMember(res, false, !res.tier.Quality.Valid), nil
	}
	if res, ok := fallbackTierMember(ctx, s.Queries, enabled, membersByTier, runtimes, required, now, idxOf); ok {
		return tierResolutionForMember(res, true, false), nil
	}
	return none, errNoTierCapacity
}

type tierRoleConfig struct {
	idx      int32
	autoPick bool
}

// tierRoleConfig loads role order and auto-pick eligibility for candidate
// tiers. Legacy role-less agents continue using model_tier's global values.
func (s *TaskService) tierRoleConfig(ctx context.Context, tiers []db.ModelTier, role string) map[string]tierRoleConfig {
	out := map[string]tierRoleConfig{}
	canonical := normalizeTierRole(role)
	if canonical == "" || !tierRoleKeyPattern.MatchString(canonical) || s == nil || s.Queries == nil {
		return out
	}
	ids := make([]pgtype.UUID, 0, len(tiers))
	for _, t := range tiers {
		ids = append(ids, t.ID)
	}
	rows, err := s.Queries.ListModelTierRoleIdxForTiers(ctx, db.ListModelTierRoleIdxForTiersParams{
		Ids: ids, Role: canonical,
	})
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[util.UUIDToString(r.TierID)] = tierRoleConfig{idx: r.Idx, autoPick: r.AutoPick}
	}
	return out
}

// effectiveTierIdx is the candidate order key: the role-specific idx when
// the tier has a row for the agent's role, else the global tier idx.
func effectiveTierIdx(t db.ModelTier, roleConfig map[string]tierRoleConfig) int32 {
	if setting, ok := roleConfig[util.UUIDToString(t.ID)]; ok {
		return setting.idx
	}
	return t.Idx
}

// tierMemberAvailability is one role-matched candidate tier+member with its
// runtime resolved for the capacity check (M-481).
type tierMemberAvailability struct {
	tier    db.ModelTier
	member  db.ModelTierMember
	runtime db.AgentRuntime
	legacy  bool // true when falling back to tier-level fields (no members)
}

// tierAvailability is kept for legacy/test compatibility.
type tierAvailability struct {
	tier    db.ModelTier
	runtime db.AgentRuntime
}

func tierResolutionForMember(av tierMemberAvailability, fallback, unverified bool) tierResolution {
	model := av.member.Model
	thinking := av.member.ThinkingLevel
	if av.legacy {
		model = av.tier.Model
		thinking = av.tier.ThinkingLevel
	}
	return tierResolution{
		runtimeID:         av.runtime.ID,
		modelOverride:     pgtype.Text{String: model, Valid: model != ""},
		thinkingLevel:     tierTextOrNull(thinking),
		serviceTier:       tierTextOrNull(av.tier.ServiceTier),
		tierID:            av.tier.ID,
		memberID:          av.member.ID,
		hasCapacity:       true,
		qualityFallback:   fallback,
		qualityUnverified: unverified,
	}
}

// tierResolutionFor builds the enqueue stamp from a legacy tier-level pick.
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
func (s *TaskService) batchTierMembersAndRuntimes(ctx context.Context, tiers []db.ModelTier) (map[string][]db.ModelTierMember, map[string]db.AgentRuntime) {
	membersByTier := map[string][]db.ModelTierMember{}
	runtimeIDs := map[string]pgtype.UUID{}
	for _, t := range tiers {
		tid := util.UUIDToString(t.ID)
		members, err := s.Queries.ListEnabledModelTierMembersByTier(ctx, t.ID)
		if err == nil && len(members) > 0 {
			membersByTier[tid] = members
			for _, m := range members {
				if m.RuntimeID.Valid {
					runtimeIDs[util.UUIDToString(m.RuntimeID)] = m.RuntimeID
				}
			}
			continue
		}
		// Legacy fallback: tier with no members behaves like pre-M481 tier.
		if t.RuntimeID.Valid {
			runtimeIDs[util.UUIDToString(t.RuntimeID)] = t.RuntimeID
		}
	}
	runtimes := map[string]db.AgentRuntime{}
	if len(runtimeIDs) == 0 {
		return membersByTier, runtimes
	}
	ids := make([]pgtype.UUID, 0, len(runtimeIDs))
	for _, id := range runtimeIDs {
		ids = append(ids, id)
	}
	rows, err := s.Queries.GetAgentRuntimes(ctx, ids)
	if err == nil {
		for _, rt := range rows {
			runtimes[util.UUIDToString(rt.ID)] = rt
		}
	}
	return membersByTier, runtimes
}

func firstTierMemberWithCapacity(ctx context.Context, q *db.Queries, tiers []db.ModelTier, membersByTier map[string][]db.ModelTierMember, runtimes map[string]db.AgentRuntime, now time.Time) (tierMemberAvailability, bool) {
	for _, t := range tiers {
		if av, ok := tierMemberCapacity(ctx, q, t, membersByTier, runtimes, now); ok {
			return av, true
		}
	}
	return tierMemberAvailability{}, false
}

func tierMemberCapacity(ctx context.Context, q *db.Queries, t db.ModelTier, membersByTier map[string][]db.ModelTierMember, runtimes map[string]db.AgentRuntime, now time.Time) (tierMemberAvailability, bool) {
	members := membersByTier[util.UUIDToString(t.ID)]
	if len(members) == 0 {
		// Legacy tier without members: use tier-level fields.
		if av, ok := tierCapacity(ctx, q, t, runtimes, now); ok {
			return tierMemberAvailability{tier: av.tier, runtime: av.runtime, legacy: true}, true
		}
		return tierMemberAvailability{}, false
	}
	for _, m := range members {
		if av, ok := memberCapacity(ctx, q, t, m, runtimes, now); ok {
			return av, true
		}
	}
	return tierMemberAvailability{}, false
}

func memberCapacity(ctx context.Context, q *db.Queries, t db.ModelTier, m db.ModelTierMember, runtimes map[string]db.AgentRuntime, now time.Time) (tierMemberAvailability, bool) {
	if !m.Enabled {
		return tierMemberAvailability{}, false
	}
	if tierCoolingDown(m.CooldownUntil, now) {
		return tierMemberAvailability{}, false
	}
	if !m.RuntimeID.Valid {
		return tierMemberAvailability{}, false
	}
	rt, ok := runtimes[util.UUIDToString(m.RuntimeID)]
	if !ok || !isRuntimeClaimable(rt) {
		return tierMemberAvailability{}, false
	}
	running, err := q.CountActiveTasksByRuntime(ctx, rt.ID)
	if err != nil {
		return tierMemberAvailability{}, false
	}
	slots := int64(m.ProviderSlots)
	if slots < 1 {
		slots = 1
	}
	if running >= slots {
		return tierMemberAvailability{}, false
	}
	return tierMemberAvailability{tier: t, member: m, runtime: rt}, true
}

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

func (s *TaskService) batchTierRuntimes(ctx context.Context, tiers []db.ModelTier) map[string]db.AgentRuntime {
	_, runtimes := s.batchTierMembersAndRuntimes(ctx, tiers)
	return runtimes
}

func firstTierWithCapacity(ctx context.Context, q *db.Queries, tiers []db.ModelTier, runtimes map[string]db.AgentRuntime, now time.Time) (tierAvailability, bool) {
	for _, t := range tiers {
		if av, ok := tierCapacity(ctx, q, t, runtimes, now); ok {
			return av, true
		}
	}
	return tierAvailability{}, false
}

func fallbackTierMember(ctx context.Context, q *db.Queries, tiers []db.ModelTier, membersByTier map[string][]db.ModelTierMember, runtimes map[string]db.AgentRuntime, required int, now time.Time, idxOf func(db.ModelTier) int32) (tierMemberAvailability, bool) {
	_, _, below := splitTiersByFloor(tiers, required)
	if len(below) == 0 {
		return tierMemberAvailability{}, false
	}
	ordered := append([]db.ModelTier(nil), below...)
	sortTiersByQualityDescWithIdx(ordered, idxOf)
	for _, t := range ordered {
		if av, ok := tierMemberCapacity(ctx, q, t, membersByTier, runtimes, now); ok {
			return av, true
		}
	}
	return tierMemberAvailability{}, false
}

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
	sortTiersByQualityDescWithIdx(tiers, func(t db.ModelTier) int32 { return t.Idx })
}

// sortTiersByQualityDescWithIdx is sortTiersByQualityDesc with a pluggable
// order key (M-497): the resolver passes the role-specific idx so the
// fallback tie-break follows the agent's role list, not the global order.
func sortTiersByQualityDescWithIdx(tiers []db.ModelTier, idxOf func(db.ModelTier) int32) {
	sort.SliceStable(tiers, func(i, j int) bool {
		qi, qj := tiers[i].Quality.Int32, tiers[j].Quality.Int32
		if qi != qj {
			return qi > qj
		}
		ii, jj := idxOf(tiers[i]), idxOf(tiers[j])
		if ii != jj {
			return ii < jj
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

// stampExplicitTier applies member traversal to one validated tier profile
// (M-481): first available member in position order wins. If all members are
// unavailable the explicit pick parks (errNoExplicitTierCapacity), no fallback.
func (s *TaskService) stampExplicitTier(ctx context.Context, tier db.ModelTier) (tierResolution, error) {
	var none tierResolution
	members, err := s.Queries.ListEnabledModelTierMembersByTier(ctx, tier.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return none, err
	}
	now := time.Now()
	if len(members) == 0 {
		// Legacy tier without members.
		if tierCoolingDown(tier.CooldownUntil, now) || !tier.RuntimeID.Valid {
			return none, errNoExplicitTierCapacity
		}
		rt, rerr := s.Queries.GetAgentRuntime(ctx, tier.RuntimeID)
		if rerr != nil || !isRuntimeClaimable(rt) {
			return none, errNoExplicitTierCapacity
		}
		running, cerr := s.Queries.CountActiveTasksByRuntime(ctx, rt.ID)
		if cerr != nil {
			return none, cerr
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
	for _, m := range members {
		if tierCoolingDown(m.CooldownUntil, now) || !m.RuntimeID.Valid {
			continue
		}
		rt, rerr := s.Queries.GetAgentRuntime(ctx, m.RuntimeID)
		if rerr != nil || !isRuntimeClaimable(rt) {
			continue
		}
		running, cerr := s.Queries.CountActiveTasksByRuntime(ctx, rt.ID)
		if cerr != nil {
			return none, cerr
		}
		slots := int64(m.ProviderSlots)
		if slots < 1 {
			slots = 1
		}
		if running >= slots {
			continue
		}
		return tierResolution{
			runtimeID:         rt.ID,
			modelOverride:     pgtype.Text{String: m.Model, Valid: m.Model != ""},
			thinkingLevel:     tierTextOrNull(m.ThinkingLevel),
			serviceTier:       tierTextOrNull(tier.ServiceTier),
			tierID:            tier.ID,
			memberID:          m.ID,
			hasCapacity:       true,
			qualityUnverified: !tier.Quality.Valid,
		}, nil
	}
	return none, errNoExplicitTierCapacity
}
