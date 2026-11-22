package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// PreparedIssueHandoff contains network-dependent data prepared before the
// caller locks the issue. Its target must still be validated in the transaction.
type PreparedIssueHandoff struct {
	agent      db.Agent
	attr       attribution.Result
	overlay    runtimeMCPOverlayData
	head       pgtype.Text
	tierParked pgtype.Bool
	// M-351: enqueue-time tier resolution, computed in Prepare (pool reads)
	// and stamped in the tx insert. queuedOnly marks full tiers: the caller
	// inserts a runtime-less queued row promotion later re-resolves.
	tierRuntimeID  pgtype.UUID
	tierModel      pgtype.Text
	tierThinking   pgtype.Text
	tierService    pgtype.Text
	tierID         pgtype.UUID
	tierQueuedOnly bool
	tierExplicit   pgtype.Bool
	// M-423: quality signal from the tier stamp (fallback = floor missed,
	// unverified = NULL quality).
	tierFallback   bool
	tierUnverified bool
}

func (s *TaskService) PrepareIssueHandoff(ctx context.Context, issue db.Issue, agentID, actorID pgtype.UUID) (PreparedIssueHandoff, error) {
	if !actorID.Valid {
		return PreparedIssueHandoff{}, fmt.Errorf("issue handoff requires a member actor")
	}
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return PreparedIssueHandoff{}, fmt.Errorf("load handoff agent: %w", err)
	}
	if agent.ArchivedAt.Valid || (!agent.RuntimeID.Valid && !agent.UseTiers && !roleRunEnabled(ctx, s, agent)) {
		return PreparedIssueHandoff{}, fmt.Errorf("handoff agent is archived or has no runtime")
	}
	attr := s.attributionForIssueTask(ctx, issue, pgtype.UUID{}, attribution.SourceCommentSource, actorID)
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		return PreparedIssueHandoff{}, err
	}
	// M-351: resolve tiers outside the tx (pool capacity reads); the tx
	// insert below only stamps.
	tierRes, tierParked, tierQueuedOnly, tierErr := s.applyTierResolutionWithOverride(ctx, agent, issue.ID, issue.Properties, TierTriggerOverride{}, actorID)
	if tierErr != nil {
		return PreparedIssueHandoff{}, tierErr
	}
	return PreparedIssueHandoff{
		agent: agent, attr: attr,
		overlay:       s.buildRuntimeMCPOverlay(ctx, attr.UserID, agent),
		head:          headShaText(s.ResolveIssueReviewSHA(ctx, issue.ID)),
		tierRuntimeID: tierRes.runtimeID, tierModel: tierRes.modelOverride, tierThinking: tierRes.thinkingLevel,
		tierService: tierRes.serviceTier, tierID: tierRes.tierID, tierQueuedOnly: tierQueuedOnly,
		tierParked: tierParked, tierExplicit: tierExplicitFlag(tierRes.tierID, TierTriggerOverride{}),
		tierFallback: tierRes.qualityFallback, tierUnverified: tierRes.qualityUnverified,
	}, nil
}

// EnqueuePreparedIssueHandoffInTx inserts the durable task without publishing
// events. The caller validates workflow, membership, invocation access and any
// squad leader relationship in the same transaction before calling it.
func (s *TaskService) EnqueuePreparedIssueHandoffInTx(ctx context.Context, q *db.Queries, issue db.Issue, squadID pgtype.UUID, prepared PreparedIssueHandoff) (db.AgentTaskQueue, error) {
	if err := guardIssueNotInTriage(ctx, q, issue.ID, OriginDerived); err != nil {
		return db.AgentTaskQueue{}, err
	}
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: prepared.agent.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("revalidate handoff agent: %w", err)
	}
	if agent.ArchivedAt.Valid || agent.RuntimeID != prepared.agent.RuntimeID || agent.UseTiers != prepared.agent.UseTiers {
		return db.AgentTaskQueue{}, fmt.Errorf("handoff agent changed during preparation")
	}
	if !agent.RuntimeID.Valid && !agent.UseTiers {
		return db.AgentTaskQueue{}, fmt.Errorf("handoff agent changed during preparation")
	}
	source, delegated, kind, ref := attributionCreateParams(prepared.attr)
	task, err := q.CreateAgentTask(ctx, db.CreateAgentTaskParams{
		ID: dbid.NewV7(), AgentID: agent.ID, RuntimeID: prepared.tierRuntimeID, IssueID: issue.ID,
		ModelOverride: prepared.tierModel, ThinkingLevelOverride: prepared.tierThinking,
		ServiceTierOverride: prepared.tierService, TierID: prepared.tierID,
		TierParked: prepared.tierParked, TierExplicit: prepared.tierExplicit,
		TierQualityFallback:   pgtype.Bool{Bool: prepared.tierFallback, Valid: prepared.tierFallback},
		TierQualityUnverified: pgtype.Bool{Bool: prepared.tierUnverified, Valid: prepared.tierUnverified},
		Priority:              priorityToInt(issue.Priority), IsLeaderTask: pgtype.Bool{Bool: squadID.Valid, Valid: squadID.Valid},
		SquadID: squadID, OriginatorUserID: prepared.attr.UserID, AccountableUserID: prepared.attr.AccountableUserID,
		RuleVersionID: prepared.attr.RuleVersionID, RuntimeMcpOverlay: prepared.overlay.Overlay,
		RuntimeConnectedApps: prepared.overlay.ConnectedApps, OriginatorSource: source,
		DelegatedFromTaskID: delegated, TriggerEvidenceKind: kind, TriggerEvidenceRefID: ref,
		HeadSha: prepared.head, FreshRerun: pgtype.Bool{Bool: false, Valid: true},
	})
	if err != nil {
		if isDuplicatePendingTaskErr(err) {
			return db.AgentTaskQueue{}, ErrDuplicatePendingTask
		}
		return db.AgentTaskQueue{}, fmt.Errorf("create handoff task: %w", err)
	}
	return task, nil
}

// HandoffAgent exposes the prepared agent for caller-side transaction
// revalidation without leaking the field's mutability.
func (p PreparedIssueHandoff) HandoffAgent() db.Agent { return p.agent }

// FinalizeIssueHandoff publishes only after the caller has committed.
func (s *TaskService) FinalizeIssueHandoff(ctx context.Context, task db.AgentTaskQueue, prepared PreparedIssueHandoff) {
	s.RecordHumanDelegation(ctx, prepared.attr)
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.NotifyTaskEnqueued(ctx, task)
}
