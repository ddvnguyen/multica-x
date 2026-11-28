package powercontextingest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// readTestPool opens the shared test database. Without one the suite skips, the
// same contract every other DB-backed package here follows.
func readTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database not reachable: %v", err)
	}
	var present bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.issue_decision_record') IS NOT NULL").Scan(&present); err != nil || !present {
		pool.Close()
		t.Skip("issue_decision_record not present (database not migrated)")
	}
	t.Cleanup(pool.Close)
	return pool
}

// readFixture builds one workspace with a project, an issue in it, and an issue
// outside any project, so the scope fallback is exercised on real rows.
type readFixture struct {
	fx        *testutil.Fixture
	projectID string
	issueID   string
	looseID   string
}

func newReadFixture(t *testing.T, pool *pgxpool.Pool) readFixture {
	t.Helper()
	fx := testutil.New(pool, "", "")
	suffix := uuid.NewString()
	fx.UserID = fx.User(t, "Ingest member", "ingest-"+suffix+"@example.invalid")
	fx.WorkspaceID = fx.Workspace(t, "Ingest workspace", "ingest-"+suffix)
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")

	projectID := fx.Project(t, "Memory dogfood", testutil.Cols{"status": "in_progress"})
	return readFixture{
		fx:        fx,
		projectID: projectID,
		issueID:   fx.Issue(t, "Compare the two planes", testutil.Cols{"project_id": projectID}),
		looseID:   fx.Issue(t, "No project here"),
	}
}

func (f readFixture) decision(t *testing.T, issueID string, over testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{
		"id":           uuid.NewString(),
		"workspace_id": f.fx.WorkspaceID,
		"issue_id":     issueID,
		"comment_id":   uuid.NewString(),
		"decided":      "Mirror human comments only.",
		"source_kind":  "comment",
		"actor_id":     f.fx.UserID,
	}
	for k, v := range over {
		cols[k] = v
	}
	return f.fx.Insert(t, "issue_decision_record", cols)
}

func TestReadMirrorsCurrentDecisionsAndHumanCommentsOnly(t *testing.T) {
	pool := readTestPool(t)
	f := newReadFixture(t, pool)

	f.decision(t, f.issueID, testutil.Cols{
		"decided":   "Project scope wins.",
		"scope":     "project",
		"rationale": "The project survives the issue.",
	})
	f.decision(t, f.looseID, testutil.Cols{"decided": "Workspace fallback."})

	// A superseded decision is not the workspace's decision any more.
	old := f.decision(t, f.issueID, testutil.Cols{"decided": "Superseded call."})
	f.decision(t, f.issueID, testutil.Cols{"decided": "Replacement call.", "supersedes_id": old})

	human := f.fx.Comment(t, f.issueID, "Use the shadow read first.")
	f.fx.Comment(t, f.issueID, "Agent reasoning that must not be mirrored.", testutil.Cols{
		"author_type": "agent",
		"author_id":   uuid.NewString(),
	})
	f.fx.Comment(t, f.issueID, "Moved to in_progress", testutil.Cols{"type": "status_change"})
	f.fx.Comment(t, f.issueID, "   ", testutil.Cols{})

	entries, err := Read(context.Background(), pool, Options{WorkspaceID: f.fx.WorkspaceID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	byMarker := map[string]Entry{}
	for _, e := range entries {
		byMarker[Marker(e)] = e
	}
	// Two current decisions on the project issue (the original is superseded), one
	// on the project-less issue, and the one human comment.
	if len(entries) != 4 {
		t.Fatalf("read %d entries, want 4: %+v", len(entries), byMarker)
	}
	for _, e := range entries {
		if e.Kind == KindDecision && e.Body == "Superseded call." {
			t.Fatal("a superseded decision was mirrored")
		}
	}
	if _, ok := byMarker[DecisionMarkerPrefix+findDecisionID(t, entries, "Replacement call.")]; !ok {
		t.Fatal("the replacement decision was dropped")
	}

	projectDecision := byMarker[DecisionMarkerPrefix+findDecisionID(t, entries, "Project scope wins.")]
	if projectDecision.ProjectID != f.projectID {
		t.Fatalf("decision project = %q, want %q", projectDecision.ProjectID, f.projectID)
	}
	if projectDecision.Kind != KindDecision || projectDecision.Rationale != "The project survives the issue." {
		t.Fatalf("decision entry = %+v", projectDecision)
	}
	if projectDecision.IssueTitle != "Compare the two planes" {
		t.Fatalf("decision issue title = %q", projectDecision.IssueTitle)
	}

	// A decision with no project_id of its own inherits the issue's.
	loose := byMarker[DecisionMarkerPrefix+findDecisionID(t, entries, "Workspace fallback.")]
	if loose.ProjectID != "" {
		t.Fatalf("issue without a project produced project %q", loose.ProjectID)
	}
	if scope, ok := Scope(loose); !ok || scope == "" {
		t.Fatalf("loose decision has no scope")
	}

	comment, ok := byMarker[CommentMarkerPrefix+human]
	if !ok {
		t.Fatalf("human comment missing: %+v", byMarker)
	}
	if comment.Body != "Use the shadow read first." || comment.AuthorType != "member" {
		t.Fatalf("comment entry = %+v", comment)
	}
	if comment.ProjectID != f.projectID {
		t.Fatalf("comment project = %q, want %q", comment.ProjectID, f.projectID)
	}
}

func findDecisionID(t *testing.T, entries []Entry, decided string) string {
	t.Helper()
	for _, e := range entries {
		if e.Kind == KindDecision && e.Body == decided {
			return e.ID
		}
	}
	t.Fatalf("no decision entry decided %q", decided)
	return ""
}

func TestReadHonoursSinceAndLimit(t *testing.T) {
	pool := readTestPool(t)
	f := newReadFixture(t, pool)

	older := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	f.decision(t, f.issueID, testutil.Cols{"decided": "Old call.", "created_at": testutil.Raw("'" + older + "'::timestamptz")})
	f.decision(t, f.issueID, testutil.Cols{"decided": "New call."})
	f.fx.Comment(t, f.issueID, "Recent comment.")

	since := time.Now().UTC().Add(-1 * time.Hour)
	entries, err := Read(context.Background(), pool, Options{WorkspaceID: f.fx.WorkspaceID, Since: since})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("since-filtered read returned %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.CreatedAt.Before(since) {
			t.Fatalf("entry %s predates --since", e.ID)
		}
	}

	limited, err := Read(context.Background(), pool, Options{WorkspaceID: f.fx.WorkspaceID, Limit: 1})
	if err != nil {
		t.Fatalf("read with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit 1 per source returned %d entries, want one decision and one comment", len(limited))
	}
	if limited[0].Kind != KindDecision || limited[1].Kind != KindComment {
		t.Fatalf("limited kinds = %q, %q", limited[0].Kind, limited[1].Kind)
	}
}

func TestReadRequiresAWorkspace(t *testing.T) {
	pool := readTestPool(t)
	if _, err := Read(context.Background(), pool, Options{}); err == nil {
		t.Fatal("read with no workspace succeeded")
	}
}

// TestReadIsUnboundedByDefault pins the fix for the oldest-first bound: a pass
// that reads the same first rows every time never drains a corpus larger than
// the bound, so zero must mean "all rows", not "a default page".
func TestReadIsUnboundedByDefault(t *testing.T) {
	pool := readTestPool(t)
	f := newReadFixture(t, pool)

	const rows = 501
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO issue_decision_record (id, workspace_id, issue_id, comment_id, decided, source_kind, actor_id)
		SELECT gen_random_uuid(), $1, $2, gen_random_uuid(), 'Bulk call ' || n, 'comment', $3
		FROM generate_series(1, $4) AS n`,
		f.fx.WorkspaceID, f.issueID, f.fx.UserID, rows); err != nil {
		t.Fatalf("seed decisions: %v", err)
	}

	entries, err := Read(context.Background(), pool, Options{WorkspaceID: f.fx.WorkspaceID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != rows {
		t.Fatalf("read %d entries, want all %d", len(entries), rows)
	}
}

func TestReadRejectsANegativeLimit(t *testing.T) {
	pool := readTestPool(t)
	if _, err := Read(context.Background(), pool, Options{WorkspaceID: "22222222-2222-2222-2222-222222222222", Limit: -1}); err == nil {
		t.Fatal("read with a negative limit succeeded")
	}
}
