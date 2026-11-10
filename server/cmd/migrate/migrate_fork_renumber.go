package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// forkMigrationRenames is the fork's migration renumbering made durable, one
// generation per upstream sync, each with its order and SQL content untouched:
//
//   - v0.8.1 put upstream on 595–616, so the fork's 595–614 moved to 617–636.
//   - v0.8.2 put upstream on 617–636, so the fork's 617–640 moved to 637–660.
//
// The generations chain: a lane that never took v0.8.1 still records 595-stems
// and lands on 637 directly, while a lane already on 617-stems (lane z) makes
// the second hop only.
//
// A lane database that ran the old numbers already has the objects and, under
// the old stems, the schema_migrations rows. Leaving those rows alone would
// make every renumbered version look unapplied and re-run its SQL — and several
// of these migrations are CREATE without IF NOT EXISTS (638_agent_question_card
// dies on `relation "agent_question_card" already exists`), which aborts the
// whole run before the server starts. So the rows are renamed in place before
// the apply loop looks at anything. A fresh database has none of the old stems,
// so the rename is a no-op there and every migration applies normally.
//
// Keeping this as data rather than 20 hand-written statements lets
// TestForkMigrationRenamesMatchFiles check each pair against the files on disk:
// a typo in a stem here would otherwise be invisible, because a rename that
// matches no row is a silent no-op.
// Each entry maps a stem some lane may still record straight to that
// migration's newest stem: a generation that has been superseded collapses into
// the newer one, which is why 595 and 617 both point at 637. Two entries sharing
// a target is correct, not a duplicate.
var forkMigrationRenames = [][2]string{
	{"595_runtime_profile_add_freebuff", "637_runtime_profile_add_freebuff"},
	{"596_agent_question_card", "638_agent_question_card"},
	{"597_agent_plan", "639_agent_plan"},
	{"598_agent_plan_review", "640_agent_plan_review"},
	{"599_agent_task_queue_blocked", "641_agent_task_queue_blocked"},
	{"600_project_settings", "642_project_settings"},
	{"601_workspace_account", "643_workspace_account"},
	{"602_task_supplement_mode", "644_task_supplement_mode"},
	{"603_workspace_account_managed", "645_workspace_account_managed"},
	{"604_workspace_account_runtime_index", "646_workspace_account_runtime_index"},
	{"605_workspace_account_daemon_id_index", "647_workspace_account_daemon_id_index"},
	{"606_decision_delivered_supplement_index", "648_decision_delivered_supplement_index"},
	{"607_native_question_answer", "649_native_question_answer"},
	{"608_native_question_answer_seq_index", "650_native_question_answer_seq_index"},
	{"609_native_question_answer_request_index", "651_native_question_answer_request_index"},
	{"610_issue_decision_response", "652_issue_decision_response"},
	{"611_issue_decision_response_request_index", "653_issue_decision_response_request_index"},
	{"612_issue_decision_response_id_index", "654_issue_decision_response_id_index"},
	{"613_decision_handoff_wakeup", "655_decision_handoff_wakeup"},
	{"614_workspace_account_engine_version", "656_workspace_account_engine_version"}, {"617_runtime_profile_add_freebuff", "637_runtime_profile_add_freebuff"},
	{"618_agent_question_card", "638_agent_question_card"},
	{"619_agent_plan", "639_agent_plan"},
	{"620_agent_plan_review", "640_agent_plan_review"},
	{"621_agent_task_queue_blocked", "641_agent_task_queue_blocked"},
	{"622_project_settings", "642_project_settings"},
	{"623_workspace_account", "643_workspace_account"},
	{"624_task_supplement_mode", "644_task_supplement_mode"},
	{"625_workspace_account_managed", "645_workspace_account_managed"},
	{"626_workspace_account_runtime_index", "646_workspace_account_runtime_index"},
	{"627_workspace_account_daemon_id_index", "647_workspace_account_daemon_id_index"},
	{"628_decision_delivered_supplement_index", "648_decision_delivered_supplement_index"},
	{"629_native_question_answer", "649_native_question_answer"},
	{"630_native_question_answer_seq_index", "650_native_question_answer_seq_index"},
	{"631_native_question_answer_request_index", "651_native_question_answer_request_index"},
	{"632_issue_decision_response", "652_issue_decision_response"},
	{"633_issue_decision_response_request_index", "653_issue_decision_response_request_index"},
	{"634_issue_decision_response_id_index", "654_issue_decision_response_id_index"},
	{"635_decision_handoff_wakeup", "655_decision_handoff_wakeup"},
	{"636_workspace_account_engine_version", "656_workspace_account_engine_version"},
	{"637_agent_plan_review_threads", "657_agent_plan_review_threads"},
	{"638_issue_workflow_continue_unfinished", "658_issue_workflow_continue_unfinished"},
	{"640_agent_question_card_trigger_comment", "660_agent_question_card_trigger_comment"},
}

// forkMigrationRenameOrder returns the table in the order it must be applied:
// highest old number first. Renames chain (595 -> 637 and 617 -> 637 both target
// this sync's stems), and one of this sync's old stems — 637 — is also a target
// of an earlier generation. Applying 617 -> 637 before 637 -> 657 would move the
// row it just wrote a second time, onto the stem of a different migration.
func forkMigrationRenameOrder() [][2]string {
	ordered := make([][2]string, len(forkMigrationRenames))
	copy(ordered, forkMigrationRenames)
	sort.SliceStable(ordered, func(i, j int) bool {
		return migrationNumber(ordered[i][0]) > migrationNumber(ordered[j][0])
	})
	return ordered
}

// migrationNumber reads the leading number of a migration stem.
func migrationNumber(stem string) int {
	head, _, _ := strings.Cut(stem, "_")
	n, err := strconv.Atoi(head)
	if err != nil {
		return -1
	}
	return n
}

// renameRenumberedForkMigrations moves each pre-sync version row onto its new
// stem on the caller's pinned connection, so the apply loop sees one
// authoritative row per migration. The NOT EXISTS guard keeps a database that
// already recorded the new stem (a fresh install, or a run interrupted between
// the rename and the next start) from colliding on the primary key.
func renameRenumberedForkMigrations(ctx context.Context, conn *pgxpool.Conn, tableIdent string) error {
	for _, pair := range forkMigrationRenameOrder() {
		oldVersion, newVersion := pair[0], pair[1]
		statement := fmt.Sprintf(
			"UPDATE %s SET version = $1 WHERE version = $2 AND NOT EXISTS (SELECT 1 FROM %s WHERE version = $1)",
			tableIdent, tableIdent,
		)
		tag, err := conn.Exec(ctx, statement, newVersion, oldVersion)
		if err != nil {
			return fmt.Errorf("rename migration %q to %q: %w", oldVersion, newVersion, err)
		}
		if tag.RowsAffected() > 0 {
			fmt.Printf("  rename %s -> %s\n", oldVersion, newVersion)
		}
	}
	return nil
}
