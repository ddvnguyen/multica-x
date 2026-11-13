package inbox

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ArchiveResolvedAskPlan archives the ask/plan inbox rows a resolution made
// stale and publishes inbox:batch-archived per member recipient so open
// clients drop them without a manual refresh (M-333).
//
// Two row shapes exist for the ask/plan types:
//
//   - Evidence rows, whose details carry the card id ('agent_question') or the
//     plan id ('plan_review'): archived whenever that card is no longer open /
//     the plan is no longer in_review. Row identity is the evidence id, so a
//     second open card on the same issue is never touched.
//   - Timeout-escalation rows (askplan_expiry), whose details carry only the
//     parked task id: archived only when taskIDs names a task whose evidence
//     is no longer open — i.e. the ask/plan itself was answered or verdicted.
//     The cancel paths pass no task ids on purpose: the expiry escalation
//     creates its row after the settle that withdraws the card, and that row
//     stays as the lead's pointer to the expired ask until it is answered.
//
// Best-effort by contract, like every inbox housekeeping path: callers log a
// failure and the rows stay stale, and the lazy sweep on list
// (ArchiveStaleAskPlanForMember) still cleans them up.
func ArchiveResolvedAskPlan(ctx context.Context, queries *db.Queries, bus *events.Bus, workspaceID, issueID string, taskIDs []string) error {
	if workspaceID == "" || issueID == "" {
		return nil
	}
	evidence, err := queries.ArchiveResolvedAskPlanInbox(ctx, db.ArchiveResolvedAskPlanInboxParams{
		WorkspaceID: util.MustParseUUID(workspaceID),
		IssueID:     util.MustParseUUID(issueID),
	})
	if err != nil {
		return err
	}
	escalation, err := queries.ArchiveResolvedAskPlanEscalationInbox(ctx, db.ArchiveResolvedAskPlanEscalationInboxParams{
		WorkspaceID: util.MustParseUUID(workspaceID),
		IssueID:     util.MustParseUUID(issueID),
		TaskIds:     taskIDs,
	})
	if err != nil {
		return err
	}
	counts := map[string]int64{}
	for _, row := range evidence {
		addMemberCount(counts, row.RecipientType, row.RecipientID)
	}
	for _, row := range escalation {
		addMemberCount(counts, row.RecipientType, row.RecipientID)
	}
	publishBatchArchived(bus, workspaceID, issueID, counts, "askplan_resolved")
	return nil
}

// ArchiveStaleAskPlanForMember archives the member's ask/plan inbox rows whose
// evidence is no longer open — the lazy half of M-333, called before the
// member's inbox list is read so rows created before their resolution was
// archived never render. It publishes inbox:batch-archived for this recipient
// so the member's other connected clients drop the rows too.
func ArchiveStaleAskPlanForMember(ctx context.Context, queries *db.Queries, bus *events.Bus, workspaceID, recipientID string) error {
	if workspaceID == "" || recipientID == "" {
		return nil
	}
	rows, err := queries.ArchiveStaleAskPlanInboxForMember(ctx, db.ArchiveStaleAskPlanInboxForMemberParams{
		WorkspaceID: util.MustParseUUID(workspaceID),
		RecipientID: util.MustParseUUID(recipientID),
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	counts := map[string]int64{}
	for _, recipientID := range rows {
		addMemberCount(counts, "member", recipientID)
	}
	publishBatchArchived(bus, workspaceID, "", counts, "askplan_lazy")
	return nil
}

// addMemberCount counts one archived row for its member recipient. Non-member
// recipients have no personal feed to notify, mirroring
// archiveStaleTaskFailedInbox's defensive recipient filter.
func addMemberCount(counts map[string]int64, recipientType string, recipientID pgtype.UUID) {
	if recipientType != "member" {
		return
	}
	counts[util.UUIDToString(recipientID)]++
}

// publishBatchArchived publishes one inbox:batch-archived per member recipient.
// The listeners layer routes the event by the payload's recipient_id, so every
// recipient gets its own event with its own count. A nil bus (bare service
// instances in tests) or an empty result skips the publish silently.
func publishBatchArchived(bus *events.Bus, workspaceID, issueID string, counts map[string]int64, reason string) {
	if bus == nil || len(counts) == 0 {
		return
	}
	total := int64(0)
	for recipientID, count := range counts {
		payload := map[string]any{
			"recipient_id": recipientID,
			"count":        count,
			"reason":       reason,
		}
		if issueID != "" {
			payload["issue_id"] = issueID
		}
		bus.Publish(events.Event{
			Type:        protocol.EventInboxBatchArchived,
			WorkspaceID: workspaceID,
			ActorType:   "system",
			Payload:     payload,
		})
		total += count
	}
	slog.Debug("archived resolved ask/plan inbox rows",
		"workspace_id", workspaceID, "issue_id", issueID,
		"row_count", total, "recipient_count", len(counts), "reason", reason)
}
