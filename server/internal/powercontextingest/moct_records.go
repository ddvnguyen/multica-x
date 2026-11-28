package powercontextingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/moct"
)

const (
	// moctPageSize is moct-be's maximum page for GET /team/v1/memory
	// (limit 1..20). Paging at the maximum keeps a large workspace to few
	// requests.
	moctPageSize = 20
	// DefaultMOCTExpert is the reader identity the mirror presents to
	// moct-be, which requires an expertId on every read. The mirror is not an
	// expert and owns no records; a fixed ref keeps its scope stable across
	// runs.
	DefaultMOCTExpert = "multica:agent:powercontext-ingest"
)

// MOCTReader is the part of moct.Client the record source needs: the paged
// search over a workspace's active records. It is an interface so a test can
// answer without a server.
type MOCTReader interface {
	Search(ctx context.Context, p moct.SearchParams) (*moct.SearchResult, error)
}

// MOCTOptions selects the MOCT record source of a pass.
type MOCTOptions struct {
	// WorkspaceID is the bare workspace id (not a ref); the D2 ref is built
	// from it. It is required: the mirror runs for one workspace at a time.
	WorkspaceID string
	// ExpertID is the reader identity moct-be requires. Empty uses
	// DefaultMOCTExpert.
	ExpertID string
	// Since drops records last updated before it. Zero means all history. It
	// filters on the record's updatedAt, because an edit is a new claim.
	Since time.Time
}

// MOCTRead is one MOCT pass: the entries to mirror, after fan-out to the
// scopes each record is visible in, and what was left out.
type MOCTRead struct {
	Entries []Entry
	// Records is the number of active records read before fan-out.
	Records int
	// ExpertHome is the number of active records left out because their home
	// is one expert. MOCT shows those to that expert alone; PowerContext has
	// no per-agent visibility, so mirroring them would show one agent's notes
	// to every agent.
	ExpertHome int
}

// MOCTScopeIndex is what placing a record needs from the database: the project
// of each task-home issue, and every project of the workspace.
type MOCTScopeIndex struct {
	// IssueProjects maps an issue id to its project id. An issue with no
	// project, or one this workspace does not hold, is absent.
	IssueProjects map[string]string
	// ProjectIDs is every project of the workspace.
	ProjectIDs []string
}

// ReadMOCTRecords lists the workspace's active MOCT records and turns them into
// mirror entries. moct-be's default status is active, so retracted and
// superseded records never come back.
func ReadMOCTRecords(ctx context.Context, pool *pgxpool.Pool, reader MOCTReader, opts MOCTOptions) (MOCTRead, error) {
	workspaceID := strings.TrimSpace(opts.WorkspaceID)
	if workspaceID == "" {
		return MOCTRead{}, fmt.Errorf("powercontext ingest needs a workspace id")
	}
	expertID := strings.TrimSpace(opts.ExpertID)
	if expertID == "" {
		expertID = DefaultMOCTExpert
	}

	records, err := listMOCTRecords(ctx, reader, moct.WorkspaceRef(workspaceID), expertID)
	if err != nil {
		return MOCTRead{}, err
	}
	index, err := loadMOCTScopeIndex(ctx, pool, workspaceID, records)
	if err != nil {
		return MOCTRead{}, err
	}
	entries, expertHome := EntriesFromMOCTRecords(records, index, workspaceID, opts.Since)
	return MOCTRead{Entries: entries, Records: len(records), ExpertHome: expertHome}, nil
}

// listMOCTRecords pages through every active record of the workspace. Pages are
// de-duplicated by id: a record edited between two page reads can shift the
// offset window, and a copy written twice is a duplicate the marker check would
// only catch after a wasted write.
func listMOCTRecords(ctx context.Context, reader MOCTReader, workspaceID, expertID string) ([]moct.Record, error) {
	var records []moct.Record
	seenIDs := map[string]bool{}
	seenCursors := map[string]bool{}
	cursor := ""
	for {
		page, err := reader.Search(ctx, moct.SearchParams{
			Scope:  moct.Scope{WorkspaceID: workspaceID, ExpertID: expertID, All: true},
			Limit:  moctPageSize,
			Cursor: cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("list MOCT records: %w", err)
		}
		if page == nil {
			return nil, fmt.Errorf("list MOCT records: empty response")
		}
		for _, record := range page.Results {
			if record.ID == "" || seenIDs[record.ID] {
				continue
			}
			seenIDs[record.ID] = true
			records = append(records, record)
		}
		next := page.NextCursor
		if next == "" {
			return records, nil
		}
		if seenCursors[next] {
			return nil, fmt.Errorf("list MOCT records: cursor %q repeated", next)
		}
		seenCursors[next] = true
		cursor = next
	}
}

// EntriesFromMOCTRecords places each record in the scopes it is visible in.
// A record can produce more than one entry: a team-home record belongs to the
// workspace and to every project of it, because a project run reads only its
// own project scope. Expert-home records are counted and left out.
func EntriesFromMOCTRecords(records []moct.Record, index MOCTScopeIndex, workspaceID string, since time.Time) ([]Entry, int) {
	var entries []Entry
	expertHome := 0
	for _, record := range records {
		if record.Status != "" && record.Status != "active" {
			continue
		}
		updated := parseMOCTTime(record.UpdatedAt)
		if !since.IsZero() && !updated.IsZero() && updated.Before(since) {
			continue
		}
		base := Entry{
			Kind:        KindRecord,
			ID:          record.ID,
			WorkspaceID: workspaceID,
			Body:        record.Body,
			RecordType:  record.Type,
			Title:       record.Title,
			CreatedAt:   updated,
		}
		for _, projectID := range recordTargets(record, index) {
			entry := base
			entry.ProjectID = projectID
			entries = append(entries, entry)
		}
		if record.Home.Kind == "expert" {
			expertHome++
		}
	}
	return entries, expertHome
}

// recordTargets is the set of project scopes a record belongs in. An empty
// project id means the workspace scope.
func recordTargets(record moct.Record, index MOCTScopeIndex) []string {
	switch record.Home.Kind {
	case "expert":
		return nil
	case "channel":
		if projectID, ok := projectFromRef(record.Home.Ref); ok {
			return []string{projectID}
		}
		return []string{""}
	case "task":
		if issueID, ok := issueFromRef(record.Home.Ref); ok {
			if projectID := index.IssueProjects[issueID]; projectID != "" {
				return []string{projectID}
			}
		}
		return []string{""}
	case "team":
		targets := make([]string, 0, len(index.ProjectIDs)+1)
		targets = append(targets, index.ProjectIDs...)
		return append(targets, "")
	default:
		return []string{""}
	}
}

func projectFromRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	id := strings.TrimPrefix(ref, moct.ProjectRefPrefix)
	if id == ref || id == "" {
		return "", false
	}
	return id, true
}

func issueFromRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	id := strings.TrimPrefix(ref, moct.IssueRefPrefix)
	if id == ref || id == "" {
		return "", false
	}
	return id, true
}

// parseMOCTTime reads moct-be's RFC3339 timestamps. An unparseable one is zero,
// which keeps the record rather than silently dropping it.
func parseMOCTTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// loadMOCTScopeIndex reads the two lookups placing a record needs, both bound to
// the workspace: task-home records resolve through their issue's project, and a
// team-home record fans out to every project of the workspace.
func loadMOCTScopeIndex(ctx context.Context, pool *pgxpool.Pool, workspaceID string, records []moct.Record) (MOCTScopeIndex, error) {
	index := MOCTScopeIndex{IssueProjects: map[string]string{}}

	projectIDs, err := loadProjectIDs(ctx, pool, workspaceID)
	if err != nil {
		return MOCTScopeIndex{}, err
	}
	index.ProjectIDs = projectIDs

	var issueIDs []string
	seen := map[string]bool{}
	for _, record := range records {
		if record.Home.Kind != "task" {
			continue
		}
		issueID, ok := issueFromRef(record.Home.Ref)
		if !ok || seen[issueID] {
			continue
		}
		seen[issueID] = true
		issueIDs = append(issueIDs, issueID)
	}
	if len(issueIDs) == 0 {
		return index, nil
	}

	rows, err := pool.Query(ctx,
		`SELECT i.id::text, COALESCE(i.project_id::text, '')
		 FROM issue i
		 WHERE i.workspace_id = $1 AND i.id::text = ANY($2)`, workspaceID, issueIDs)
	if err != nil {
		return MOCTScopeIndex{}, fmt.Errorf("read issue projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var issueID, projectID string
		if err := rows.Scan(&issueID, &projectID); err != nil {
			return MOCTScopeIndex{}, fmt.Errorf("scan issue project: %w", err)
		}
		if projectID != "" {
			index.IssueProjects[issueID] = projectID
		}
	}
	if err := rows.Err(); err != nil {
		return MOCTScopeIndex{}, fmt.Errorf("read issue projects: %w", err)
	}
	return index, nil
}

func loadProjectIDs(ctx context.Context, pool *pgxpool.Pool, workspaceID string) ([]string, error) {
	rows, err := pool.Query(ctx,
		`SELECT id::text FROM project WHERE workspace_id = $1 ORDER BY id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read workspace projects: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan workspace project: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read workspace projects: %w", err)
	}
	return ids, nil
}
