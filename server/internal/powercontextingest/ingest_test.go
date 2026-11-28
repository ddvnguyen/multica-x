package powercontextingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/powercontext"
)

// fakePlane is an httptest stand-in for the PowerContext routes this package
// uses, shaped like a real 1.6.0: /v1/scopes maps a Multica ref to a
// server-generated id, and that id — never the ref — is what the memory routes
// accept. It keeps what was remembered per scope so a second pass can be
// asserted to write nothing.
type fakePlane struct {
	mu         sync.Mutex
	scopes     map[string][]string
	kinds      map[string][]string
	ids        map[string]string
	searchFail bool
	rememberTo string // when set, /v1/memory/remember answers 422
	server     *httptest.Server
}

func newFakePlane(t *testing.T) *fakePlane {
	t.Helper()
	f := &fakePlane{scopes: map[string][]string{}, kinds: map[string][]string{}, ids: map[string]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePlane) client(t *testing.T) *powercontext.Client {
	t.Helper()
	client, err := powercontext.NewClient(powercontext.Config{BaseURL: f.server.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// scopeID is the id the plane hands out for a ref. Stable per ref, so a test
// can look a scope up the way the callers speak about it.
func (f *fakePlane) scopeID(ref string) string {
	if id, ok := f.ids[ref]; ok {
		return id
	}
	id := fmt.Sprintf("scp_%d", len(f.ids)+1)
	f.ids[ref] = id
	return id
}

func (f *fakePlane) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.URL.Path {
	case "/v1/scopes":
		query := r.URL.Query().Get("query")
		if r.URL.Query().Get("query_field") == "scope_id" {
			writeJSON(w, map[string]any{"items": []any{}, "next_cursor": nil})
			return
		}
		writeJSON(w, map[string]any{"items": []map[string]any{{
			"scope_id":            f.scopeID(query),
			"external_references": []map[string]any{{"kind": "multica-project", "value": query}},
		}}, "next_cursor": nil})
	case "/v1/memory/search":
		if f.searchFail {
			http.Error(w, `{"error":{"code":"unavailable","message":"down"}}`, http.StatusServiceUnavailable)
			return
		}
		var body struct {
			ScopeID string `json:"scope_id"`
			Query   string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits := []map[string]any{}
		for _, text := range f.scopes[body.ScopeID] {
			if body.Query == "" || strings.Contains(text, body.Query) {
				hits = append(hits, map[string]any{
					"memory": map[string]any{"text": text, "state": "active"},
					"score":  1, "matched_by": []string{"text"},
				})
			}
		}
		writeJSON(w, map[string]any{"mode": "text", "hits": hits})
	case "/v1/memory/remember":
		var body struct {
			ScopeID string `json:"scope_id"`
			Kind    string `json:"kind"`
			Text    string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":{"code":"invalid","message":"bad body"}}`, http.StatusBadRequest)
			return
		}
		if f.rememberTo != "" && strings.Contains(body.Text, f.rememberTo) {
			http.Error(w, `{"error":{"code":"invalid","message":"rejected"}}`, http.StatusUnprocessableEntity)
			return
		}
		f.scopes[body.ScopeID] = append(f.scopes[body.ScopeID], body.Text)
		f.kinds[body.ScopeID] = append(f.kinds[body.ScopeID], body.Kind)
		writeJSON(w, map[string]any{"changed": true, "records": []map[string]any{{"text": body.Text, "state": "active"}}})
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// texts returns what a ref's scope holds, resolving it the way the ingest does.
func (f *fakePlane) texts(ref string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.scopes[f.scopeID(ref)]...)
}

func decisionEntry(projectID string) Entry {
	return Entry{
		Kind:        KindDecision,
		ID:          "11111111-1111-1111-1111-111111111111",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		IssueID:     "33333333-3333-3333-3333-333333333333",
		IssueTitle:  "Memory dogfood",
		ProjectID:   projectID,
		CreatedAt:   time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC),
		Body:        "Mirror human comments only.",
		Rationale:   "Agent comments bury the decision.",
	}
}

func commentEntry(projectID string) Entry {
	return Entry{
		Kind:        KindComment,
		ID:          "44444444-4444-4444-4444-444444444444",
		WorkspaceID: "22222222-2222-2222-2222-222222222222",
		IssueID:     "33333333-3333-3333-3333-333333333333",
		IssueTitle:  "Memory dogfood",
		ProjectID:   projectID,
		AuthorType:  "member",
		CreatedAt:   time.Date(2026, 11, 27, 11, 0, 0, 0, time.UTC),
		Body:        "Ship the shadow read first.",
	}
}

func TestIngestWritesEachEntryOnce(t *testing.T) {
	plane := newFakePlane(t)
	client := plane.client(t)
	project := "55555555-5555-5555-5555-555555555555"
	entries := []Entry{decisionEntry(project), commentEntry("")}

	first, err := Ingest(context.Background(), client, entries)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.Written != 2 || first.Skipped != 0 || first.Failed != 0 {
		t.Fatalf("first pass = %+v, want 2 written", first)
	}
	if first.Decisions != 1 || first.Comments != 1 {
		t.Fatalf("first pass sources = %d decisions, %d comments", first.Decisions, first.Comments)
	}

	// The project-scoped entry lands in the project scope; the entry with no
	// project falls back to the workspace scope, exactly as the read path does.
	projectTexts := plane.texts(powercontext.ProjectScope(project))
	if len(projectTexts) != 1 {
		t.Fatalf("project scope holds %d entries, want 1", len(projectTexts))
	}
	workspaceTexts := plane.texts(powercontext.WorkspaceScope(entries[1].WorkspaceID))
	if len(workspaceTexts) != 1 {
		t.Fatalf("workspace scope holds %d entries, want 1", len(workspaceTexts))
	}

	second, err := Ingest(context.Background(), client, entries)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Written != 0 || second.Skipped != 2 || second.Failed != 0 {
		t.Fatalf("second pass = %+v, want 2 skipped and nothing written", second)
	}
	if got := len(plane.texts(powercontext.ProjectScope(project))); got != 1 {
		t.Fatalf("rerun wrote a duplicate: project scope holds %d", got)
	}
}

func TestIngestRecordsTheRememberedKind(t *testing.T) {
	plane := newFakePlane(t)
	if _, err := Ingest(context.Background(), plane.client(t), []Entry{decisionEntry(""), commentEntry("")}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	scope := powercontext.WorkspaceScope(decisionEntry("").WorkspaceID)
	plane.mu.Lock()
	defer plane.mu.Unlock()
	got := plane.kinds[plane.scopeID(scope)]
	if len(got) != 2 || got[0] != "decision" || got[1] != "fact" {
		t.Fatalf("kinds = %v, want [decision fact]", got)
	}
}

func TestIngestDoesNotWriteWhenTheDedupeSearchFails(t *testing.T) {
	plane := newFakePlane(t)
	plane.searchFail = true

	summary, err := Ingest(context.Background(), plane.client(t), []Entry{commentEntry("")})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if summary.Written != 0 || summary.Failed != 1 {
		t.Fatalf("summary = %+v, want one failure and no write", summary)
	}
	if got := plane.texts(powercontext.WorkspaceScope(commentEntry("").WorkspaceID)); len(got) != 0 {
		t.Fatalf("wrote %d entries without a successful dedupe read", len(got))
	}
	if len(summary.Failures) != 1 || !strings.Contains(summary.Failures[0], CommentMarkerPrefix) {
		t.Fatalf("failures = %v, want one naming the entry marker", summary.Failures)
	}
}

func TestIngestKeepsGoingAfterARejectedWrite(t *testing.T) {
	plane := newFakePlane(t)
	plane.rememberTo = CommentMarkerPrefix

	summary, err := Ingest(context.Background(), plane.client(t), []Entry{commentEntry(""), decisionEntry("")})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if summary.Written != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %+v, want one written and one failed", summary)
	}
	if got := plane.texts(powercontext.WorkspaceScope(commentEntry("").WorkspaceID)); len(got) != 1 {
		t.Fatalf("scope holds %d entries, want the one that succeeded", len(got))
	}
}

func TestIngestFailsAnEntryWithNoScope(t *testing.T) {
	plane := newFakePlane(t)
	entry := commentEntry("")
	entry.WorkspaceID = ""

	summary, err := Ingest(context.Background(), plane.client(t), []Entry{entry})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if summary.Failed != 1 || summary.Written != 0 {
		t.Fatalf("summary = %+v, want one failure", summary)
	}
	if !strings.Contains(summary.Failures[0], "no project or workspace id") {
		t.Fatalf("failure = %q, want the missing-scope reason", summary.Failures[0])
	}
}

func TestEntryTextCarriesMarkerAndRefs(t *testing.T) {
	entry := decisionEntry("55555555-5555-5555-5555-555555555555")
	text := EntryText(entry)
	for _, want := range []string{
		DecisionMarkerPrefix + entry.ID,
		"issue: multica:issue:" + entry.IssueID,
		"project: multica:project:" + entry.ProjectID,
		"recorded: 2026-11-27T10:00:00Z",
		"decided:\nMirror human comments only.",
		"rationale:\nAgent comments bury the decision.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("entry text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "author:") {
		t.Fatalf("a decision has no author line:\n%s", text)
	}

	comment := EntryText(commentEntry(""))
	if !strings.Contains(comment, "comment:\nShip the shadow read first.") {
		t.Fatalf("comment text = %q", comment)
	}
	if !strings.Contains(comment, "author: member") {
		t.Fatalf("comment text has no author:\n%s", comment)
	}
}

func TestEntryTextClampsTheBodyWithoutSplittingARune(t *testing.T) {
	entry := commentEntry("")
	entry.Body = strings.Repeat("é", bodyMax)

	text := EntryText(entry)
	bodyStart := strings.Index(text, "comment:\n")
	if bodyStart < 0 {
		t.Fatalf("no body in %q", text)
	}
	body := strings.TrimSuffix(text[bodyStart+len("comment:\n"):], "\n")
	if !strings.HasSuffix(body, "…") {
		t.Fatalf("clamped body has no ellipsis: %q", body)
	}
	plain := strings.TrimSuffix(body, "…")
	if len(plain) > bodyMax {
		t.Fatalf("clamped body is %d bytes, want at most %d", len(plain), bodyMax)
	}
	if len(plain) < bodyMax-1 {
		t.Fatalf("clamped body is %d bytes, want the whole budget kept", len(plain))
	}
	if !utf8.ValidString(plain) {
		t.Fatal("clamp split a rune")
	}
}

func TestMarkerFollowsTheSourceKind(t *testing.T) {
	if got := Marker(decisionEntry("")); got != DecisionMarkerPrefix+decisionEntry("").ID {
		t.Fatalf("decision marker = %q", got)
	}
	if got := Marker(commentEntry("")); got != CommentMarkerPrefix+commentEntry("").ID {
		t.Fatalf("comment marker = %q", got)
	}
	record := Entry{Kind: KindRecord, ID: "mem_1"}
	if got := Marker(record); got != RecordMarkerPrefix+"mem_1" {
		t.Fatalf("record marker = %q", got)
	}
}

func TestScopePrefersTheProject(t *testing.T) {
	project := "55555555-5555-5555-5555-555555555555"
	if scope, ok := Scope(decisionEntry(project)); !ok || scope != powercontext.ProjectScope(project) {
		t.Fatalf("scope = %q, %v", scope, ok)
	}
	entry := commentEntry("")
	entry.ProjectID = ""
	if scope, ok := Scope(entry); !ok || scope != powercontext.WorkspaceScope(entry.WorkspaceID) {
		t.Fatalf("fallback scope = %q, %v", scope, ok)
	}
	if _, ok := Scope(Entry{WorkspaceID: ""}); ok {
		t.Fatal("an entry with no ids reported a scope")
	}
}

func TestPowerContextKind(t *testing.T) {
	if got := PowerContextKind(decisionEntry("")); got != "decision" {
		t.Fatalf("decision kind = %q", got)
	}
	if got := PowerContextKind(commentEntry("")); got != "fact" {
		t.Fatalf("comment kind = %q", got)
	}
}

func TestClampTextKeepsShortTextIntact(t *testing.T) {
	if got := clampText("short", 10); got != "short" {
		t.Fatalf("clampText = %q", got)
	}
	if got := clampText(fmt.Sprintf("%d", 7), 5); got != "7" {
		t.Fatalf("clampText = %q", got)
	}
}
