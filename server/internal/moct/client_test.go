package moct

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	testWorkspace = "multica:ws:9f1c2a44-0000-4000-8000-000000000001"
	testExpert    = "multica:agent:7d5b3e11-0000-4000-8000-000000000002"
	testIssue     = "multica:issue:aa01c9de-0000-4000-8000-000000000003"
	testProject   = "multica:project:bb22f0aa-0000-4000-8000-000000000005"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClientRejectsInvalidBaseURL(t *testing.T) {
	for _, base := range []string{"", "127.0.0.1:8110", "://nope", "ftp://moct"} {
		if _, err := NewClient(Config{BaseURL: base}); err == nil {
			t.Errorf("NewClient(%q) succeeded, want error", base)
		}
	}
}

func TestInjectBuildsQueryAndDecodes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/team/v1/memory/inject" {
			t.Fatalf("path = %s, want /team/v1/memory/inject", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("workspaceId") != testWorkspace {
			t.Errorf("workspaceId = %q", q.Get("workspaceId"))
		}
		if q.Get("expertId") != testExpert {
			t.Errorf("expertId = %q", q.Get("expertId"))
		}
		if q.Get("taskRef") != testIssue {
			t.Errorf("taskRef = %q", q.Get("taskRef"))
		}
		if q.Get("channelRef") != testProject {
			t.Errorf("channelRef = %q", q.Get("channelRef"))
		}
		if q.Get("layerTokens") != "500" {
			t.Errorf("layerTokens = %q", q.Get("layerTokens"))
		}
		io.WriteString(w, `{
			"entries":[{"id":"mem_1","type":"decision","title":"D","body":"b",
				"attrs":{"decidedBy":"x"},"home":{"kind":"task","ref":"`+testIssue+`"},
				"priority":80,"pinned":false,"tokens":17}],
			"omitted":1,"duplicates":0,"conflicts":0,"tokens":17,"layerTokens":500,
			"index":[{"home":{"kind":"task","ref":"`+testIssue+`"},"more":1,
				"items":[{"id":"mem_2","type":"decision","title":"D2","tokens":9,"pinned":false}]}]
		}`)
	})

	got, err := c.Inject(context.Background(), InjectParams{
		WorkspaceID: testWorkspace,
		ExpertID:    testExpert,
		TaskRef:     testIssue,
		ChannelRef:  testProject,
		LayerTokens: 500,
	})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(got.Entries))
	}
	if got.Entries[0].Home.Ref != testIssue {
		t.Errorf("entry home ref = %q, want %q", got.Entries[0].Home.Ref, testIssue)
	}
	if got.Entries[0].Tokens != 17 {
		t.Errorf("entry tokens = %d, want 17", got.Entries[0].Tokens)
	}
	if got.Omitted != 1 || got.LayerTokens != 500 {
		t.Errorf("omitted/layerTokens = %d/%d, want 1/500", got.Omitted, got.LayerTokens)
	}
	if len(got.Index) != 1 || got.Index[0].Items[0].ID != "mem_2" {
		t.Errorf("index = %+v, want one layer with mem_2", got.Index)
	}
}

func TestInjectRequiresScopeAndSkipsRequest(t *testing.T) {
	var hits int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
	})
	if _, err := c.Inject(context.Background(), InjectParams{ExpertID: testExpert}); err == nil {
		t.Fatal("Inject without workspaceId succeeded, want error")
	}
	if _, err := c.Inject(context.Background(), InjectParams{WorkspaceID: testWorkspace}); err == nil {
		t.Fatal("Inject without expertId succeeded, want error")
	}
	if hits != 0 {
		t.Errorf("server hit %d times, want 0", hits)
	}
}

func TestSearchBuildsQueryAndDecodes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/team/v1/memory" {
			t.Fatalf("path = %s, want /team/v1/memory", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("workspaceId") != testWorkspace || q.Get("expertId") != testExpert {
			t.Errorf("scope = %q/%q", q.Get("workspaceId"), q.Get("expertId"))
		}
		if q.Get("tasks") != testIssue {
			t.Errorf("tasks = %q", q.Get("tasks"))
		}
		if q.Get("channels") != testProject {
			t.Errorf("channels = %q", q.Get("channels"))
		}
		if q.Get("q") != "alpha beta" {
			t.Errorf("q = %q", q.Get("q"))
		}
		if q.Get("types") != "decision,convention" {
			t.Errorf("types = %q", q.Get("types"))
		}
		if q.Get("status") != "all" {
			t.Errorf("status = %q", q.Get("status"))
		}
		if q.Get("limit") != "5" || q.Get("cursor") != "10" {
			t.Errorf("limit/cursor = %q/%q", q.Get("limit"), q.Get("cursor"))
		}
		io.WriteString(w, `{
			"results":[{"id":"mem_1","type":"decision","title":"alpha",
				"home":{"kind":"task","ref":"`+testIssue+`"},"status":"active",
				"tags":[{"kind":"task","ref":"`+testIssue+`"}]}],
			"total":1,"nextCursor":null
		}`)
	})

	got, err := c.Search(context.Background(), SearchParams{
		Scope: Scope{
			WorkspaceID: testWorkspace,
			ExpertID:    testExpert,
			Channels:    []string{testProject},
			Tasks:       []string{testIssue},
		},
		Query:  "alpha beta",
		Types:  []string{"decision", "convention"},
		Status: "all",
		Limit:  5,
		Cursor: "10",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Total != 1 || len(got.Results) != 1 {
		t.Fatalf("total/results = %d/%d, want 1/1", got.Total, len(got.Results))
	}
	if got.Results[0].ID != "mem_1" || got.Results[0].Status != "active" {
		t.Errorf("record = %+v", got.Results[0])
	}
	if got.NextCursor != "" {
		t.Errorf("nextCursor = %q, want empty for JSON null", got.NextCursor)
	}
	if len(got.Results[0].Tags) != 1 || got.Results[0].Tags[0].Ref != testIssue {
		t.Errorf("tags = %+v, want task tag %q", got.Results[0].Tags, testIssue)
	}
}

func TestGetPostsIDsAndScope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/team/v1/memory/get" {
			t.Fatalf("path = %s, want /team/v1/memory/get", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("tasks") != testIssue || q.Get("workspaceId") != testWorkspace {
			t.Errorf("scope query = %v", q)
		}
		var body struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(body.IDs) != 2 || body.IDs[0] != "mem_1" {
			t.Errorf("ids = %v", body.IDs)
		}
		io.WriteString(w, `{
			"records":[{"id":"mem_1","type":"decision","home":{"kind":"task","ref":"`+testIssue+`"}}],
			"missing":["mem_9"]
		}`)
	})

	got, err := c.Get(context.Background(), []string{"mem_1", "mem_9"}, Scope{
		WorkspaceID: testWorkspace,
		ExpertID:    testExpert,
		Tasks:       []string{testIssue},
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Records) != 1 || got.Records[0].ID != "mem_1" {
		t.Errorf("records = %+v", got.Records)
	}
	if len(got.Missing) != 1 || got.Missing[0] != "mem_9" {
		t.Errorf("missing = %v, want [mem_9]", got.Missing)
	}
}

func TestGetValidatesIDCount(t *testing.T) {
	var hits int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { hits++ })
	scope := Scope{WorkspaceID: testWorkspace, ExpertID: testExpert}
	if _, err := c.Get(context.Background(), nil, scope); err == nil {
		t.Error("Get with no ids succeeded, want error")
	}
	if _, err := c.Get(context.Background(), []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}, scope); err == nil {
		t.Error("Get with 11 ids succeeded, want error")
	}
	if hits != 0 {
		t.Errorf("server hit %d times, want 0", hits)
	}
}

func TestNoteBodyCarriesRefsAndNoTaskRefField(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/team/v1/memory" {
			t.Fatalf("%s %s, want POST /team/v1/memory", r.Method, r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body %q: %v", raw, err)
		}
		if _, ok := body["taskRef"]; ok {
			t.Errorf("body carries taskRef: %s (D2 issues live in home, and moct rejects non-task: refs)", raw)
		}
		if !strings.Contains(string(body["home"]), testIssue) {
			t.Errorf("home = %s, want issue ref", body["home"])
		}
		if !strings.Contains(string(body["workspaceId"]), "multica:ws:") {
			t.Errorf("workspaceId = %s", body["workspaceId"])
		}
		if !strings.Contains(string(body["actor"]), "multica:agent:") {
			t.Errorf("actor = %s", body["actor"])
		}
		if string(body["confirmNew"]) != "true" {
			t.Errorf("confirmNew = %s, want true", body["confirmNew"])
		}
		if _, ok := body["pinned"]; ok {
			t.Errorf("pinned sent as false: %s", body["pinned"])
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{
			"record":{"id":"mem_1","type":"decision","status":"pending",
				"home":{"kind":"task","ref":"`+testIssue+`"}},
			"nearMatches":[],
			"notice":"Saved as pending: it loads once an orchestrator or the human approves it."
		}`)
	})

	got, err := c.Note(context.Background(), NoteParams{
		Type:        "decision",
		Title:       "Seam decision",
		Body:        "carry refs in home",
		Home:        Home{Kind: "task", Ref: testIssue},
		WorkspaceID: testWorkspace,
		Actor:       Actor{Kind: "member", ID: testExpert},
		ConfirmNew:  true,
	})
	if err != nil {
		t.Fatalf("Note: %v", err)
	}
	if got.Record == nil || got.Record.Status != "pending" {
		t.Fatalf("record = %+v, want pending record", got.Record)
	}
	if got.Notice == "" {
		t.Error("notice = empty, want pending explanation")
	}
}

func TestNoteNearMatchHoldsWrite(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{
			"record":null,
			"nearMatches":[{"id":"mem_1","type":"decision","title":"dup",
				"home":{"kind":"task","ref":"`+testIssue+`"},"status":"active"}],
			"notice":null
		}`)
	})
	got, err := c.Note(context.Background(), NoteParams{
		Type:        "decision",
		Title:       "dup",
		Body:        "x",
		Home:        Home{Kind: "task", Ref: testIssue},
		WorkspaceID: testWorkspace,
		Actor:       Actor{Kind: "member", ID: testExpert},
	})
	if err != nil {
		t.Fatalf("Note: %v", err)
	}
	if got.Record != nil {
		t.Fatalf("record = %+v, want nil (write held back)", got.Record)
	}
	if len(got.NearMatches) != 1 || got.NearMatches[0].ID != "mem_1" {
		t.Errorf("nearMatches = %+v, want mem_1", got.NearMatches)
	}
}

func TestAPIErrorMirrorsMoctEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"ok":false,"code":"INVALID_INPUT","error":"expertId is required"}`)
	})
	_, err := c.Search(context.Background(), SearchParams{
		Scope: Scope{WorkspaceID: testWorkspace, ExpertID: testExpert},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest || apiErr.Code != "INVALID_INPUT" {
		t.Errorf("APIError = %+v", apiErr)
	}
	if apiErr.Message != "expertId is required" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Error("4xx reported as ErrUnavailable")
	}
}

func TestServerErrorCarriesStatusWithoutEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>bad gateway</html>")
	})
	_, err := c.Search(context.Background(), SearchParams{
		Scope: Scope{WorkspaceID: testWorkspace, ExpertID: testExpert},
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway || apiErr.Message != "<html>bad gateway</html>" {
		t.Errorf("APIError = %+v", apiErr)
	}
}

func TestUnavailableWhenMoctIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // moct-be down: connection refused

	c, err := NewClient(Config{BaseURL: base})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Inject(context.Background(), InjectParams{
		WorkspaceID: testWorkspace,
		ExpertID:    testExpert,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("transport failure reported as APIError: %+v", apiErr)
	}
}

func TestSearchRejectsEmptyScope(t *testing.T) {
	var hits int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { hits++ })
	if _, err := c.Search(context.Background(), SearchParams{
		Scope: Scope{WorkspaceID: testWorkspace},
	}); err == nil {
		t.Fatal("Search without expertId succeeded, want error")
	}
	if hits != 0 {
		t.Errorf("server hit %d times, want 0", hits)
	}
}

// A mis-configured base URL must fail at construction, not on first use.
func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c, err := NewClient(Config{BaseURL: "http://127.0.0.1:8110/"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != "http://127.0.0.1:8110" {
		t.Errorf("baseURL = %q, want trailing slash trimmed", c.baseURL)
	}
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Host != "127.0.0.1:8110" {
		t.Errorf("baseURL did not parse: %q (%v)", c.baseURL, err)
	}
}
