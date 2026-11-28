// Package powercontextingest mirrors the curated records Multica already holds
// into a PowerContext scope (M-616 item 3, option A of the Architect's
// decision): the workspace's current issue decisions and its human-authored
// issue comments.
//
// The seam (docs/powercontext-seam.md) only ever wrote the run's own handoff
// entry. A backfilled corpus is what a prepare can recall from on its first
// read, and it is the other half of the dogfood: the comparison is only fair if
// both planes started from the same knowledge.
//
// Two rules shape what is mirrored:
//
//   - Human comments only. An agent writes pages of reasoning per run; copying
//     that into the memory plane would bury the two or three lines a person
//     actually decided. Decisions are already curated by whoever recorded them,
//     so those are mirrored whatever the recorder.
//   - Idempotent by source id. Each entry carries a marker naming the comment
//     or decision it came from, and the entry is skipped when a search finds
//     that marker. A search that fails skips the entry instead of writing a
//     possible duplicate: a gap is retried by the next pass, a duplicate is
//     injected forever.
//
// The third source the card named — active MOCT records — has no enumerated
// list endpoint (moct.Client injects, searches, gets by id, notes, retracts),
// so it is deliberately absent rather than guessed at. Same for native
// PowerContext handoff endpoints: this writes through /v1/memory/remember,
// whose payload is the one the seam already verified.
package powercontextingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/moct"
	"github.com/multica-ai/multica/server/internal/powercontext"
)

const (
	// DefaultLimit bounds rows read per source in one pass. A pass is meant to
	// be rerun, so a workspace larger than this drains over several runs
	// instead of holding one long transaction.
	DefaultLimit = 500
	// bodyMax bounds one copied body. PowerContext caps a memory body at 8192
	// bytes and the entry also carries refs and a marker; 6000 leaves room for
	// the header without splitting a rune.
	bodyMax = 6000
	// titleMax bounds the issue title copied into the entry header.
	titleMax = 200
)

// Marker prefixes. They are the idempotency key and a valid search query: each
// names exactly one source row, so the dedupe read cannot match another entry.
const (
	DecisionMarkerPrefix = "decision:/multica/decision/"
	CommentMarkerPrefix  = "comment:/multica/comment/"
)

// Entry kinds, as recorded in the entry text. They also carry the remembered
// entry's own kind: a decision is a decision whatever its source.
const (
	KindDecision = "decision"
	KindComment  = "comment"
)

// Entry is one source record eligible for mirroring.
type Entry struct {
	Kind string
	// ID is the source row's id — the decision id or the comment id.
	ID          string
	WorkspaceID string
	IssueID     string
	IssueTitle  string
	// ProjectID selects the scope; empty falls back to the workspace scope.
	ProjectID string
	// AuthorType is set for comments and empty for decisions.
	AuthorType string
	CreatedAt  time.Time
	// Body is the decided text, or the comment content.
	Body string
	// Rationale is the decision's rationale, empty for comments.
	Rationale string
}

// Marker is the entry's idempotency key, written into its text and searched for
// before it is written again.
func Marker(e Entry) string {
	if e.Kind == KindDecision {
		return DecisionMarkerPrefix + e.ID
	}
	return CommentMarkerPrefix + e.ID
}

// Scope is the PowerContext scope the entry belongs in: the same
// project-else-workspace rule the read path uses, so a backfilled entry is
// recallable by the runs that will read it.
func Scope(e Entry) (string, bool) {
	return powercontext.ScopeForTask(e.ProjectID, e.WorkspaceID)
}

// PowerContextKind is the remembered entry's kind: a decision is one, and a
// human comment is a fact of record rather than a decision Multica ever
// recorded as such.
func PowerContextKind(e Entry) string {
	if e.Kind == KindDecision {
		return "decision"
	}
	return "fact"
}

// EntryText renders one entry: the marker, the refs it belongs to, and the
// copied body. Plain text, because it is recalled as context rather than
// parsed.
func EntryText(e Entry) string {
	var b strings.Builder
	b.WriteString(Marker(e))
	b.WriteString("\n")
	if e.IssueID != "" {
		if title := strings.TrimSpace(e.IssueTitle); title != "" {
			fmt.Fprintf(&b, "issue: %s (%s)\n", moct.IssueRef(e.IssueID), clampText(title, titleMax))
		} else {
			fmt.Fprintf(&b, "issue: %s\n", moct.IssueRef(e.IssueID))
		}
	}
	if e.ProjectID != "" {
		fmt.Fprintf(&b, "project: %s\n", moct.ProjectRef(e.ProjectID))
	}
	if e.AuthorType != "" {
		fmt.Fprintf(&b, "author: %s\n", e.AuthorType)
	}
	if !e.CreatedAt.IsZero() {
		fmt.Fprintf(&b, "recorded: %s\n", e.CreatedAt.UTC().Format(time.RFC3339))
	}
	if e.Kind == KindDecision {
		b.WriteString("decided:\n")
		b.WriteString(clampText(strings.TrimSpace(e.Body), bodyMax))
		b.WriteString("\n")
		if rationale := strings.TrimSpace(e.Rationale); rationale != "" {
			b.WriteString("rationale:\n")
			b.WriteString(clampText(rationale, bodyMax))
			b.WriteString("\n")
		}
		return b.String()
	}
	b.WriteString("comment:\n")
	b.WriteString(clampText(strings.TrimSpace(e.Body), bodyMax))
	b.WriteString("\n")
	return b.String()
}

// Summary reports one pass. Written plus Skipped plus Failed equals the number
// of entries the pass read.
type Summary struct {
	Decisions int
	Comments  int
	Written   int
	Skipped   int
	Failed    int
	// Failures names the first few failures, each as "<marker>: <reason>".
	Failures []string
}

const maxReportedFailures = 10

func (s *Summary) count(e Entry) {
	if e.Kind == KindDecision {
		s.Decisions++
	} else {
		s.Comments++
	}
}

func (s *Summary) fail(e Entry, err error) {
	s.Failed++
	if len(s.Failures) < maxReportedFailures {
		s.Failures = append(s.Failures, fmt.Sprintf("%s: %v", Marker(e), err))
	}
}

// Writer is the part of powercontext.Client this package needs. It is an
// interface so a test can answer without a server.
type Writer interface {
	Search(ctx context.Context, p powercontext.SearchParams) (*powercontext.SearchResult, error)
	Remember(ctx context.Context, req powercontext.RememberRequest) (*powercontext.RememberResult, error)
}

// Ingest writes every entry that is not already in its scope.
//
// One entry's failure never stops the pass: a bad scope or a rejected body is
// recorded in the summary and the next entry is tried. The returned error is
// reserved for a caller-side problem, so today it is always nil.
func Ingest(ctx context.Context, w Writer, entries []Entry) (Summary, error) {
	var summary Summary
	for _, e := range entries {
		summary.count(e)
		scope, ok := Scope(e)
		if !ok {
			summary.fail(e, fmt.Errorf("no project or workspace id to scope"))
			continue
		}
		marker := Marker(e)
		existing, err := w.Search(ctx, powercontext.SearchParams{ScopeID: scope, Query: marker, Limit: 3})
		if err != nil {
			// Not written: a duplicate cannot be removed, a missing entry is
			// retried by the next pass.
			summary.fail(e, fmt.Errorf("dedupe search in %s: %w", scope, err))
			continue
		}
		if existing != nil && containsMarker(existing, marker) {
			summary.Skipped++
			continue
		}
		if _, err := w.Remember(ctx, powercontext.RememberRequest{
			ScopeID: scope,
			Kind:    PowerContextKind(e),
			Text:    EntryText(e),
		}); err != nil {
			summary.fail(e, fmt.Errorf("remember in %s: %w", scope, err))
			continue
		}
		summary.Written++
	}
	return summary, nil
}

func containsMarker(result *powercontext.SearchResult, marker string) bool {
	for _, text := range result.Texts() {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Options selects what one pass reads.
type Options struct {
	// WorkspaceID is required: the mirror runs for one workspace at a time.
	WorkspaceID string
	// Since limits the pass to records created at or after it. Zero means all
	// history. It is a convenience for incremental runs, not the idempotency
	// mechanism — the marker is.
	Since time.Time
	// Limit bounds each source. Zero uses DefaultLimit.
	Limit int
}

// Read loads the eligible entries, oldest first, from decisions and human
// comments. Only current decisions are read (a superseded record is not the
// workspace's decision any more), and only comments a member wrote.
func Read(ctx context.Context, pool *pgxpool.Pool, opts Options) ([]Entry, error) {
	workspaceID := strings.TrimSpace(opts.WorkspaceID)
	if workspaceID == "" {
		return nil, fmt.Errorf("powercontext ingest needs a workspace id")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	var since any
	if !opts.Since.IsZero() {
		since = opts.Since
	}

	decisions, err := readDecisions(ctx, pool, workspaceID, since, limit)
	if err != nil {
		return nil, err
	}
	comments, err := readComments(ctx, pool, workspaceID, since, limit)
	if err != nil {
		return nil, err
	}
	return append(decisions, comments...), nil
}

const decisionsQuery = `
SELECT d.id::text,
       d.workspace_id::text,
       d.issue_id::text,
       COALESCE(i.title, ''),
       COALESCE(d.project_id::text, i.project_id::text, ''),
       d.decided,
       d.rationale,
       d.created_at
FROM issue_decision_record d
JOIN issue i ON i.id = d.issue_id
WHERE d.workspace_id = $1
  AND NOT EXISTS (
      SELECT 1 FROM issue_decision_record replacement
      WHERE replacement.supersedes_id = d.id
  )
  AND ($2::timestamptz IS NULL OR d.created_at >= $2)
ORDER BY d.created_at ASC, d.id ASC
LIMIT $3`

const commentsQuery = `
SELECT c.id::text,
       i.workspace_id::text,
       c.issue_id::text,
       COALESCE(i.title, ''),
       COALESCE(i.project_id::text, ''),
       c.author_type,
       c.content,
       c.created_at
FROM comment c
JOIN issue i ON i.id = c.issue_id
WHERE i.workspace_id = $1
  AND c.author_type = 'member'
  AND c.type = 'comment'
  AND btrim(c.content) <> ''
  AND ($2::timestamptz IS NULL OR c.created_at >= $2)
ORDER BY c.created_at ASC, c.id ASC
LIMIT $3`

func readDecisions(ctx context.Context, pool *pgxpool.Pool, workspaceID string, since any, limit int) ([]Entry, error) {
	rows, err := pool.Query(ctx, decisionsQuery, workspaceID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("read issue decisions: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.IssueID, &e.IssueTitle, &e.ProjectID,
			&e.Body, &e.Rationale, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan issue decision: %w", err)
		}
		e.Kind = KindDecision
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read issue decisions: %w", err)
	}
	return entries, nil
}

func readComments(ctx context.Context, pool *pgxpool.Pool, workspaceID string, since any, limit int) ([]Entry, error) {
	rows, err := pool.Query(ctx, commentsQuery, workspaceID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("read issue comments: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.IssueID, &e.IssueTitle, &e.ProjectID,
			&e.AuthorType, &e.Body, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan issue comment: %w", err)
		}
		e.Kind = KindComment
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read issue comments: %w", err)
	}
	return entries, nil
}

// clampText bounds s by bytes without splitting a rune.
func clampText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
