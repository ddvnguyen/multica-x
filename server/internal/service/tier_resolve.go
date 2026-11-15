package service

import (
	"context"
	"errors"
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
}

// errNoTierCapacity signals "agent uses tiers but no selected tier has a free
// provider slot right now". Callers keep the task queued (retry on the next
// capacity change), never fail it: a full tier is backpressure, not an error.
var errNoTierCapacity = errors.New("no selected tier has capacity")

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

// tierCoolingDown reports whether a tier's provider is inside its cooldown
// window. A NULL or expired deadline is not cooling: the tier re-enters idx
// order by itself at the first resolve after cooldown_until.
func tierCoolingDown(until pgtype.Timestamptz, now time.Time) bool {
	return until.Valid && until.Time.After(now)
}

// resolveAgentTier picks the first usable tier for a use_tiers agent at
// enqueue (M-351). Candidates are the agent's selected tiers that are enabled,
// ordered by tier idx — index 0 runs first while it still has provider_slots
// free. A tier qualifies only when its runtime is online (fresh heartbeat)
// and its dispatched/running count on that runtime is below provider_slots.
// A cooling tier (M-352: cooldown_until still in the future) is skipped
// whatever its capacity, so the next task falls to the next idx instead of
// retrying a provider that is refusing. Returns errNoTierCapacity when every
// candidate is cooling, offline or full so the caller can leave the task
// queued; a use_tiers=false agent never reaches here and keeps the legacy
// agent.RuntimeID path untouched.
func (s *TaskService) resolveAgentTier(ctx context.Context, agent db.Agent) (tierResolution, error) {
	var none tierResolution
	selections, err := s.Queries.ListAgentTiers(ctx, agent.ID)
	if err != nil {
		return none, err
	}
	enabled := make([]db.ModelTier, 0, len(selections))
	for _, sel := range selections {
		if sel.Enabled {
			enabled = append(enabled, sel)
		}
	}
	if len(enabled) == 0 {
		return none, errNoTierCapacity
	}
	runtimeIDs := make([]pgtype.UUID, 0, len(enabled))
	for _, t := range enabled {
		if t.RuntimeID.Valid {
			runtimeIDs = append(runtimeIDs, t.RuntimeID)
		}
	}
	runtimes := map[string]db.AgentRuntime{}
	if len(runtimeIDs) > 0 {
		rows, err := s.Queries.GetAgentRuntimes(ctx, runtimeIDs)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return none, err
		}
		for _, rt := range rows {
			runtimes[util.UUIDToString(rt.ID)] = rt
		}
	}
	now := time.Now()
	for _, t := range enabled {
		if tierCoolingDown(t.CooldownUntil, now) {
			continue
		}
		rt, ok := runtimes[util.UUIDToString(t.RuntimeID)]
		if !t.RuntimeID.Valid || !ok {
			continue
		}
		if !isRuntimeClaimable(rt) {
			continue
		}
		running, err := s.Queries.CountActiveTasksByRuntime(ctx, rt.ID)
		if err != nil {
			return none, err
		}
		slots := int64(t.ProviderSlots)
		if slots < 1 {
			slots = 1
		}
		if running >= slots {
			continue
		}
		return tierResolution{
			runtimeID:     rt.ID,
			modelOverride: pgtype.Text{String: t.Model, Valid: t.Model != ""},
			thinkingLevel: tierTextOrNull(t.ThinkingLevel),
			serviceTier:   tierTextOrNull(t.ServiceTier),
			tierID:        t.ID,
			hasCapacity:   true,
		}, nil
	}
	return none, errNoTierCapacity
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
