package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/inbox"
	"github.com/multica-ai/multica/server/internal/moct"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const (
	// moctPendingSweepInterval is one poll every five minutes: pending records
	// need a human within the hour, not the second, and each round is one
	// workspace scan plus one moct-be read per workspace. Overlapped rounds
	// are dropped by runPeriodicSweep.
	moctPendingSweepInterval = 5 * time.Minute
	// moctPendingSweepBatch bounds one round's moct-be reads. Pending records
	// are rare — one per unreviewed worker note — so 20 pages of 20 is
	// headroom, not a drain rate.
	moctPendingSweepBatch = 20
)

// runMoctPendingSweeper is M-570: when a record lands pending, the workspace
// managers (owners/admins — the Owner among them) get one memory_pending
// inbox row linking to Settings → MOCT → Memory. Server-side polling, not a
// daemon hook: moct-be is polled from here, so no writer runs on the daemon
// path (the brief forbids daemon writes for this) and both the main stack
// and lane z share the one moct-be behind MOCT_BASE_URL.
//
// The three orchestrator agents (Lead, Orchestrator, Claude Architect) are
// not addressed as inbox recipients: they have no stable per-workspace agent
// rows to deliver to, and an agent inbox is not actionable. They see pending
// through the agent page's Memory tab and the per-run injected-memory view.
func runMoctPendingSweeper(ctx context.Context, queries *db.Queries, bus *events.Bus) {
	runPeriodicSweep(ctx, moctPendingSweepInterval, func() {
		if notified, err := sweepMoctPending(ctx, queries, bus); err != nil {
			slog.Warn("moct pending sweeper: round failed", "error", err)
		} else if notified > 0 {
			slog.Info("moct pending sweeper: notified", "records", notified)
		}
	})
}

// sweepMoctPending is one round: every workspace, its still-pending records,
// minus the ones already announced, delivered to its managers.
func sweepMoctPending(ctx context.Context, queries *db.Queries, bus *events.Bus) (int, error) {
	baseURL := strings.TrimSpace(os.Getenv("MOCT_BASE_URL"))
	if baseURL == "" {
		return 0, nil
	}
	client, err := moct.NewClient(moct.Config{BaseURL: baseURL})
	if err != nil {
		return 0, nil
	}
	workspaceIDs, err := queries.ListMoctSweepWorkspaceIDs(ctx)
	if err != nil {
		return 0, err
	}
	notified := 0
	for _, workspaceID := range workspaceIDs {
		n, err := sweepMoctPendingWorkspace(ctx, queries, bus, client, workspaceID)
		if err != nil {
			slog.Warn("moct pending sweeper: workspace failed",
				"workspace_id", util.UUIDToString(workspaceID), "error", err)
			continue
		}
		notified += n
	}
	return notified, nil
}

func sweepMoctPendingWorkspace(ctx context.Context, queries *db.Queries, bus *events.Bus, client *moct.Client, workspaceID pgtype.UUID) (int, error) {
	wsRef := "multica:ws:" + util.UUIDToString(workspaceID)
	// The sweeper reads as the workspace, not as a user: All drops the tag
	// filter but keeps the workspace, so one read sees every pending record.
	search, err := client.Search(ctx, moct.SearchParams{
		Scope:  moct.Scope{WorkspaceID: wsRef, ExpertID: "multica:sweeper:memory-pending", All: true},
		Status: "pending",
		Limit:  moctPendingSweepBatch,
	})
	if err != nil {
		return 0, err
	}
	announced, err := queries.ListMoctNotifiedRecordIDs(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(announced))
	for _, id := range announced {
		seen[id] = true
	}
	recipients, err := service.ListWorkspaceManagerNotificationRecipients(ctx, queries, workspaceID)
	if err != nil {
		return 0, err
	}
	if len(recipients) == 0 {
		return 0, nil
	}
	notified := 0
	for _, record := range search.Results {
		if record.Status != "pending" || seen[record.ID] {
			continue
		}
		title := record.Title
		if title == "" {
			title = "Untitled record"
		}
		details, _ := json.Marshal(map[string]string{
			"memory_id": record.ID,
			"status":    "pending",
			"tab":       "moct-memory",
		})
		emitted := false
		for _, recipient := range recipients {
			item, inserted, err := inbox.Deliver(ctx, queries, bus, inbox.Notification{Params: db.CreateInboxItemParams{
				ID:            dbid.NewV7(),
				WorkspaceID:   workspaceID,
				RecipientType: recipient.Type,
				RecipientID:   recipient.ID,
				Type:          "memory_pending",
				Severity:      "action_required",
				IssueID:       pgtype.UUID{},
				Title:         "Memory record waiting for approval: " + title,
				Body:          util.StrToText(record.Body),
				ActorType:     util.StrToText("system"),
				ActorID:       pgtype.UUID{},
				Details:       details,
			}})
			if err != nil {
				slog.Warn("moct pending sweeper: deliver failed",
					"workspace_id", util.UUIDToString(workspaceID),
					"record_id", record.ID, "error", err)
				continue
			}
			if inserted {
				emitted = true
				_ = item
			}
		}
		// The gate is stamped when at least one manager heard: a workspace
		// whose managers all left still re-announces next round rather than
		// silently dropping the record.
		if rows, err := queries.MarkMoctPendingNotified(ctx, db.MarkMoctPendingNotifiedParams{
			WorkspaceID: workspaceID,
			RecordID:    record.ID,
		}); err != nil {
			slog.Warn("moct pending sweeper: stamp failed",
				"workspace_id", util.UUIDToString(workspaceID),
				"record_id", record.ID, "error", err)
		} else if rows > 0 && emitted {
			notified++
		}
		seen[record.ID] = true
	}
	return notified, nil
}
