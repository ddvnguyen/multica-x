package powercontextingest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/moct"
	"github.com/multica-ai/multica/server/internal/powercontext"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// fakeMOCTReader answers GET /team/v1/memory from a cursor -> page map, and
// records every request so a test can pin the paging contract.
type fakeMOCTReader struct {
	pages map[string]*moct.SearchResult
	err   error
	calls []moct.SearchParams
}

func (f *fakeMOCTReader) Search(_ context.Context, p moct.SearchParams) (*moct.SearchResult, error) {
	f.calls = append(f.calls, p)
	if f.err != nil {
		return nil, f.err
	}
	page, ok := f.pages[p.Cursor]
	if !ok {
		return &moct.SearchResult{}, nil
	}
	return page, nil
}

func moctRecord(id, homeKind, homeRef string) moct.Record {
	return moct.Record{
		ID:        id,
		Type:      "decision",
		Title:     "Title of " + id,
		Body:      "Body of " + id,
		Home:      moct.Home{Kind: homeKind, Ref: homeRef},
		Status:    "active",
		UpdatedAt: "2026-11-27T12:00:00.000Z",
	}
}

func TestListMOCTRecordsPagesAtTheMaximumAndDeduplicates(t *testing.T) {
	reader := &fakeMOCTReader{pages: map[string]*moct.SearchResult{
		"":  {Results: []moct.Record{moctRecord("mem_1", "team", ""), moctRecord("mem_2", "team", "")}, Total: 4, NextCursor: "2"},
		"2": {Results: []moct.Record{moctRecord("mem_2", "team", ""), moctRecord("mem_3", "team", "")}, Total: 4, NextCursor: "4"},
		"4": {Results: nil, Total: 4},
	}}
	records, err := listMOCTRecords(context.Background(), reader, "multica:ws:ws-1", "multica:agent:mirror")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("read %d records, want 3 (a repeated id is one record)", len(records))
	}
	if len(reader.calls) != 3 {
		t.Fatalf("made %d requests, want 3 pages", len(reader.calls))
	}
	for i, call := range reader.calls {
		if call.Limit != moctPageSize {
			t.Fatalf("page %d limit = %d, want %d", i, call.Limit, moctPageSize)
		}
		if !call.All {
			t.Fatalf("page %d did not ask for the whole workspace", i)
		}
		// Empty status is the request relying on moct-be's default, which is
		// "active" — the ingest must never mirror history.
		if call.Status != "" {
			t.Fatalf("page %d status = %q, want the default active", i, call.Status)
		}
		if call.WorkspaceID != "multica:ws:ws-1" || call.ExpertID != "multica:agent:mirror" {
			t.Fatalf("page %d scope = %+v", i, call.Scope)
		}
	}
	if reader.calls[1].Cursor != "2" || reader.calls[2].Cursor != "4" {
		t.Fatalf("cursors = %q, %q", reader.calls[1].Cursor, reader.calls[2].Cursor)
	}
}

func TestListMOCTRecordsRejectsARepeatedCursor(t *testing.T) {
	reader := &fakeMOCTReader{pages: map[string]*moct.SearchResult{
		"":  {Results: []moct.Record{moctRecord("mem_1", "team", "")}, NextCursor: "2"},
		"2": {Results: []moct.Record{moctRecord("mem_2", "team", "")}, NextCursor: "2"},
	}}
	if _, err := listMOCTRecords(context.Background(), reader, "multica:ws:ws-1", "multica:agent:mirror"); err == nil {
		t.Fatal("a cursor that never advances was followed")
	}
}

func TestEntriesFromMOCTRecordsPlacesEachHomeKind(t *testing.T) {
	index := MOCTScopeIndex{
		IssueProjects: map[string]string{"issue-1": "project-a"},
		ProjectIDs:    []string{"project-a", "project-b"},
	}
	records := []moct.Record{
		moctRecord("mem_channel", "channel", moct.ProjectRef("project-c")),
		moctRecord("mem_task", "task", moct.IssueRef("issue-1")),
		moctRecord("mem_loose", "task", moct.IssueRef("issue-unknown")),
		moctRecord("mem_team", "team", "multica:ws:ws-1"),
		moctRecord("mem_expert", "expert", "multica:agent:someone"),
		moctRecord("mem_bad_channel", "channel", "not-a-ref"),
	}

	entries, expertHome := EntriesFromMOCTRecords(records, index, "ws-1", time.Time{})
	if expertHome != 1 {
		t.Fatalf("expert-home records = %d, want 1", expertHome)
	}
	got := map[string][]string{}
	for _, e := range entries {
		got[e.ID] = append(got[e.ID], e.ProjectID)
	}
	want := map[string][]string{
		"mem_channel":     {"project-c"},
		"mem_task":        {"project-a"},
		"mem_loose":       {""},
		"mem_team":        {"project-a", "project-b", ""},
		"mem_bad_channel": {""},
	}
	for id, projects := range want {
		if strings.Join(got[id], ",") != strings.Join(projects, ",") {
			t.Fatalf("%s lands in %v, want %v", id, got[id], projects)
		}
	}
	if _, ok := got["mem_expert"]; ok {
		t.Fatal("an expert-home record was mirrored")
	}
	// Every copy of a team-home record carries the same idempotency key.
	for _, e := range entries {
		if e.ID == "mem_team" && Marker(e) != RecordMarkerPrefix+"mem_team" {
			t.Fatalf("team copy marker = %q", Marker(e))
		}
	}
}

func TestEntriesFromMOCTRecordsSkipsHistoryAndHonoursSince(t *testing.T) {
	retracted := moctRecord("mem_retracted", "team", "multica:ws:ws-1")
	retracted.Status = "retracted"
	old := moctRecord("mem_old", "team", "multica:ws:ws-1")
	old.UpdatedAt = "2026-11-01T00:00:00Z"
	fresh := moctRecord("mem_fresh", "team", "multica:ws:ws-1")

	entries, _ := EntriesFromMOCTRecords(
		[]moct.Record{retracted, old, fresh},
		MOCTScopeIndex{},
		"ws-1",
		time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
	)
	if len(entries) != 1 || entries[0].ID != "mem_fresh" {
		t.Fatalf("entries = %+v, want only mem_fresh", entries)
	}
	if entries[0].CreatedAt.UTC().Format(time.RFC3339) != "2026-11-27T12:00:00Z" {
		t.Fatalf("entry time = %v, want the record's updatedAt", entries[0].CreatedAt)
	}
}

func TestReadMOCTRecordsWrapsATransportFailure(t *testing.T) {
	reader := &fakeMOCTReader{err: moct.ErrUnavailable}
	_, err := ReadMOCTRecords(context.Background(), nil, reader, MOCTOptions{WorkspaceID: "ws-1"})
	if err == nil {
		t.Fatal("a failed listing returned no error")
	}
	if !errors.Is(err, moct.ErrUnavailable) {
		t.Fatalf("error %v does not wrap ErrUnavailable", err)
	}
}

func TestReadMOCTRecordsRequiresAWorkspace(t *testing.T) {
	if _, err := ReadMOCTRecords(context.Background(), nil, &fakeMOCTReader{}, MOCTOptions{}); err == nil {
		t.Fatal("read with no workspace succeeded")
	}
}

func TestIngestMirrorsARecordOncePerScope(t *testing.T) {
	plane := newFakePlane(t)
	index := MOCTScopeIndex{ProjectIDs: []string{"55555555-5555-5555-5555-555555555555"}}
	entries, _ := EntriesFromMOCTRecords(
		[]moct.Record{moctRecord("mem_team", "team", "multica:ws:"+workspaceIDOf(t))},
		index,
		workspaceIDOf(t),
		time.Time{},
	)
	if len(entries) != 2 {
		t.Fatalf("team record produced %d entries, want project + workspace", len(entries))
	}

	first, err := Ingest(context.Background(), plane.client(t), entries)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.Written != 2 || first.Skipped != 0 || first.Failed != 0 {
		t.Fatalf("first pass = %+v, want both copies written", first)
	}
	if first.Records != 1 {
		t.Fatalf("records counted %d, want the distinct record once", first.Records)
	}
	for _, scope := range []string{
		powercontext.ProjectScope("55555555-5555-5555-5555-555555555555"),
		powercontext.WorkspaceScope(workspaceIDOf(t)),
	} {
		if got := len(plane.texts(scope)); got != 1 {
			t.Fatalf("%s holds %d entries, want 1", scope, got)
		}
	}

	second, err := Ingest(context.Background(), plane.client(t), entries)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Written != 0 || second.Skipped != 2 {
		t.Fatalf("second pass = %+v, want both copies skipped", second)
	}
}

func TestIngestSkipsARecordWhoseDedupeSearchFails(t *testing.T) {
	plane := newFakePlane(t)
	plane.searchFail = true
	entry := Entry{Kind: KindRecord, ID: "mem_1", WorkspaceID: workspaceIDOf(t), Body: "Body.", RecordType: "decision"}

	summary, err := Ingest(context.Background(), plane.client(t), []Entry{entry})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if summary.Written != 0 || summary.Failed != 1 || summary.Records != 1 {
		t.Fatalf("summary = %+v, want one record failed and nothing written", summary)
	}
	if got := plane.texts(powercontext.WorkspaceScope(workspaceIDOf(t))); len(got) != 0 {
		t.Fatalf("wrote %d entries without a successful dedupe read", len(got))
	}
}

func TestRecordEntryTextCarriesTypeTitleAndBody(t *testing.T) {
	entry := Entry{
		Kind:        KindRecord,
		ID:          "mem_1",
		WorkspaceID: workspaceIDOf(t),
		ProjectID:   "55555555-5555-5555-5555-555555555555",
		RecordType:  "decision",
		Title:       "Mirror the records too",
		Body:        "List them by scope.",
		CreatedAt:   time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC),
	}
	text := EntryText(entry)
	for _, want := range []string{
		RecordMarkerPrefix + "mem_1",
		"project: multica:project:55555555-5555-5555-5555-555555555555",
		"type: decision",
		"title: Mirror the records too",
		"updated: 2026-11-27T12:00:00Z",
		"List them by scope.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("record text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "recorded:") {
		t.Fatalf("a record's timestamp is updatedAt, not created:\n%s", text)
	}
}

func TestPowerContextKindForRecords(t *testing.T) {
	decision := Entry{Kind: KindRecord, RecordType: "decision"}
	if got := PowerContextKind(decision); got != "decision" {
		t.Fatalf("record decision kind = %q", got)
	}
	for _, recordType := range []string{"rule", "convention", "how_to_work", "task", "branch"} {
		if got := PowerContextKind(Entry{Kind: KindRecord, RecordType: recordType}); got != "fact" {
			t.Fatalf("%s record kind = %q, want fact", recordType, got)
		}
	}
}

// TestReadMOCTRecordsResolvesHomesAgainstTheWorkspace covers the two lookups the
// pure function cannot: a task home's project and a team home's project list.
func TestReadMOCTRecordsResolvesHomesAgainstTheWorkspace(t *testing.T) {
	pool := readTestPool(t)
	f := newReadFixture(t, pool)
	otherProject := f.fx.Project(t, "Second project", testutil.Cols{})

	reader := &fakeMOCTReader{pages: map[string]*moct.SearchResult{
		"": {Results: []moct.Record{
			moctRecord("mem_task", "task", moct.IssueRef(f.issueID)),
			moctRecord("mem_loose", "task", moct.IssueRef(f.looseID)),
			moctRecord("mem_team", "team", moct.WorkspaceRef(f.fx.WorkspaceID)),
			moctRecord("mem_expert", "expert", moct.ExpertRef("someone")),
		}},
	}}
	read, err := ReadMOCTRecords(context.Background(), pool, reader, MOCTOptions{WorkspaceID: f.fx.WorkspaceID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Records != 4 {
		t.Fatalf("read %d records, want 4", read.Records)
	}
	if read.ExpertHome != 1 {
		t.Fatalf("expert-home count = %d, want 1", read.ExpertHome)
	}

	projects := map[string][]string{}
	for _, e := range read.Entries {
		projects[e.ID] = append(projects[e.ID], e.ProjectID)
	}
	if got := projects["mem_task"]; len(got) != 1 || got[0] != f.projectID {
		t.Fatalf("task-home record lands in %v, want the issue's project %q", got, f.projectID)
	}
	if got := projects["mem_loose"]; len(got) != 1 || got[0] != "" {
		t.Fatalf("task-home record without a project lands in %v, want the workspace", got)
	}
	want := map[string]bool{f.projectID: true, otherProject: true, "": true}
	team := projects["mem_team"]
	if len(team) != 3 {
		t.Fatalf("team-home record lands in %v, want workspace plus both projects", team)
	}
	for _, projectID := range team {
		if !want[projectID] {
			t.Fatalf("team-home record landed in unexpected scope %q", projectID)
		}
	}
	if _, ok := projects["mem_expert"]; ok {
		t.Fatal("an expert-home record was mirrored")
	}
}

// workspaceIDOf is the workspace the fixture-free tests scope to. It never
// reaches a database: those tests assert on the entries, not on rows.
func workspaceIDOf(t *testing.T) string {
	t.Helper()
	return "22222222-2222-2222-2222-222222222222"
}
