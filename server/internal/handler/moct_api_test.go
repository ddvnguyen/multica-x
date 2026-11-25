package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestTaskMemoryReportRequiresIssueReadAccess(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Private report", "private-report-"+uuid.NewString())
	issueID := dbfx.Issue(t, "Unreadable run", testutil.Cols{"workspace_id": workspaceID})
	req := testutil.WithHeaders(httptest.NewRequest(http.MethodGet, "/", nil),
		"X-User-ID", testUserID, "X-Workspace-ID", workspaceID)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", issueID)
	route.URLParams.Add("taskId", uuid.NewString())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.GetTaskMemoryReport).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unreadable issue memory report status=%d body=%s, want 404", recorder.Code, recorder.Body.String())
	}
}

func TestMemoryReportPart(t *testing.T) {
	tests := []struct {
		name, result string
		wantNote     string
		wantBody     string
		wantNull     bool
	}{
		{name: "memory reason is returned", result: `{"init_context":{"parts":[{"part":"instructions"},{"part":"memory","tokens":0,"note":"MOCT seam is off"}]}}`, wantNote: "MOCT seam is off"},
		{name: "injected entries are returned", result: `{"init_context":{"parts":[{"part":"memory","entries":[{"id":"r1","type":"rule","title":"Keep it clear","body":"Use plain language."}]}]}}`, wantBody: "Use plain language."},
		{name: "missing report", result: `{}`, wantNull: true},
		{name: "malformed report", result: `not json`, wantNull: true},
		{name: "report without memory", result: `{"init_context":{"parts":[{"part":"task"}]}}`, wantNull: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := memoryReportPart(json.RawMessage(tc.result))
			if tc.wantNull {
				if string(got) != "null" {
					t.Fatalf("memory = %s, want null", got)
				}
				return
			}
			var part map[string]any
			if err := json.Unmarshal(got, &part); err != nil {
				t.Fatalf("decode memory part: %v", err)
			}
			if tc.wantNote != "" && part["note"] != tc.wantNote {
				t.Fatalf("note = %v, want %q", part["note"], tc.wantNote)
			}
			if tc.wantBody != "" {
				entries, ok := part["entries"].([]any)
				if !ok || len(entries) != 1 {
					t.Fatalf("entries = %v, want body %q", part["entries"], tc.wantBody)
				}
				entry, ok := entries[0].(map[string]any)
				if !ok || entry["body"] != tc.wantBody {
					t.Fatalf("entry = %v, want body %q", entries[0], tc.wantBody)
				}
			}
		})
	}
}

// M-570: the human-only purge gate must match exactly the route the proxy
// serves, or agents keep a delete path (too narrow) or humans lose it (too
// wide).
func TestIsMoctPurgeRequest(t *testing.T) {
	ws := "11111111-1111-4111-8111-111111111111"
	purge := "/api/workspaces/" + ws + "/moct/team/v1/memory/mem_1/purge"
	for _, tc := range []struct {
		name, method, path string
		want               bool
	}{
		{"purge matches", http.MethodPost, purge, true},
		{"approve is not purge", http.MethodPost, "/api/workspaces/" + ws + "/moct/team/v1/memory/mem_1/approve", false},
		{"bad id is not purge", http.MethodPost, "/api/workspaces/" + ws + "/moct/team/v1/memory/mem%201/purge", false},
		{"get purge is not purge", http.MethodGet, purge, false},
		{"purge without id is not purge", http.MethodPost, "/api/workspaces/" + ws + "/moct/team/v1/memory/purge", false},
		{"list is not purge", http.MethodGet, "/api/workspaces/" + ws + "/moct/team/v1/memory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if got := isMoctPurgeRequest(req); got != tc.want {
				t.Fatalf("isMoctPurgeRequest(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// M-570: agent task tokens bind X-User-ID to the runtime owner's user, so
// create-note needs the same machine-credential gate as purge — otherwise an
// agent files human-authored notes that skip the approval gate.
func TestIsMoctCreateRequest(t *testing.T) {
	ws := "11111111-1111-4111-8111-111111111111"
	collection := "/api/workspaces/" + ws + "/moct/team/v1/memory"
	for _, tc := range []struct {
		name, method, path string
		want               bool
	}{
		{"create matches", http.MethodPost, collection, true},
		{"get list is not create", http.MethodGet, collection, false},
		{"get subpath is not create", http.MethodPost, collection + "/get", false},
		{"purge is not create", http.MethodPost, collection + "/mem_1/purge", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if got := isMoctCreateRequest(req); got != tc.want {
				t.Fatalf("isMoctCreateRequest(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}
