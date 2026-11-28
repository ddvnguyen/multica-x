package powercontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testScopeID is the server-generated id a Multica ref resolves to. PowerContext
// never uses a ref as an id: it keeps the ref as an external reference and hands
// out scp_... ids, so every route test serves the listing that maps one to the
// other.
const testScopeID = "scp_test_1"

// serveScopes answers GET /v1/scopes for one ref and reports whether it did.
// Counting through calls lets a test assert the listing is cached per client.
func serveScopes(w http.ResponseWriter, r *http.Request, ref string, calls *atomic.Int32) bool {
	if r.URL.Path != "/v1/scopes" {
		return false
	}
	if calls != nil {
		calls.Add(1)
	}
	_, _ = fmt.Fprintf(w, `{"items":[{"scope_id":%q,"external_references":[{"kind":"multica-project","value":%q}]}],"next_cursor":null}`,
		testScopeID, ref)
	return true
}

// TestPrepareSendsContract pins the wire contract the seam depends on: the
// route, the four fields that drive recall, and the bearer header when a token
// is configured. A refactor that renames a JSON field here silently empties
// every pre-run block, so the shape is asserted rather than inferred.
func TestPrepareSendsContract(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "multica:project:prj_1", nil) {
			return
		}
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"schema":"prepare/1","status":"ready","content":"## memory\n\nkeep it async","content_bytes":27}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL, Token: "tok_1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.Prepare(context.Background(), PrepareRequest{
		ScopeID:  "multica:project:prj_1",
		Query:    "how should a token refresh work",
		MaxBytes: 4096,
		Assembly: &Assembly{Format: "markdown", Sections: []Section{{Family: "memory", Limit: 5}}},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/context/prepare" {
		t.Fatalf("request = %s %s, want POST /v1/context/prepare", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok_1" {
		t.Fatalf("Authorization = %q, want the configured bearer", gotAuth)
	}
	// The ref is resolved, never sent as an id: a real server answers
	// scope_not_found for anything but its own scp_... id.
	if gotBody["scope_id"] != testScopeID {
		t.Fatalf("scope_id = %v, want the resolved Scope id %q", gotBody["scope_id"], testScopeID)
	}
	if gotBody["query"] != "how should a token refresh work" {
		t.Fatalf("query = %v", gotBody["query"])
	}
	if gotBody["max_bytes"] != float64(4096) {
		t.Fatalf("max_bytes = %v", gotBody["max_bytes"])
	}
	assembly, ok := gotBody["assembly"].(map[string]any)
	if !ok || assembly["format"] != "markdown" {
		t.Fatalf("assembly = %v", gotBody["assembly"])
	}
	// The receipt is a diagnostic: the pre-run path must not ask for it.
	if _, sent := gotBody["include_receipt"]; sent {
		t.Fatalf("prepare sent include_receipt; the pre-run path does not request it")
	}
	if got.Text() != "## memory\n\nkeep it async" || got.ContentBytes != 27 {
		t.Fatalf("content = %q (%d bytes)", got.Text(), got.ContentBytes)
	}
}

func TestPrepareOmitsAuthorizationWithoutToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "multica:ws:ws_1", nil) {
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"empty","content":null,"content_bytes":0}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Prepare(context.Background(), PrepareRequest{ScopeID: "multica:ws:ws_1"}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want none for an unauthenticated loopback server", gotAuth)
	}
}

// TestPreparedIsEmpty covers every shape an empty answer can take: the
// documented "empty" status, a null content on a ready status, and an empty
// string. All three must yield no block — a block with no entries would only
// cost tokens and tell the agent memory exists when it does not.
func TestPreparedIsEmpty(t *testing.T) {
	empty := ""
	ready := "## memory"
	cases := []struct {
		name string
		in   *Prepared
		want bool
	}{
		{"nil", nil, true},
		{"empty status", &Prepared{Status: "empty"}, true},
		{"ready null content", &Prepared{Status: "ready", Content: nil}, true},
		{"ready empty string", &Prepared{Status: "ready", Content: &empty}, true},
		{"ready content", &Prepared{Status: "ready", Content: &ready}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.IsEmpty(); got != tc.want {
				t.Fatalf("IsEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
	if (&Prepared{Status: "empty"}).Text() != "" {
		t.Fatalf("Text() on an empty result should be \"\"")
	}
}

func TestClampMaxBytes(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, DefaultMaxBytes},
		{-5, DefaultMaxBytes},
		{1, MinMaxBytes},
		{511, MinMaxBytes},
		{512, 512},
		{24000, 24000},
		{MaxMaxBytes, MaxMaxBytes},
		{999999, MaxMaxBytes},
	}
	for _, tc := range cases {
		if got := ClampMaxBytes(tc.in); got != tc.want {
			t.Fatalf("ClampMaxBytes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestPrepareClampsAndSendsBudget(t *testing.T) {
	var gotMax float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "s", nil) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["max_bytes"].(float64); ok {
			gotMax = v
		}
		_, _ = w.Write([]byte(`{"status":"ready","content":"x","content_bytes":1}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Prepare(context.Background(), PrepareRequest{ScopeID: "s", MaxBytes: 10}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if gotMax != MinMaxBytes {
		t.Fatalf("max_bytes = %v, want the floor %d", gotMax, MinMaxBytes)
	}
}

func TestPrepareRejectsMissingScope(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Prepare(context.Background(), PrepareRequest{ScopeID: "  "}); err == nil {
		t.Fatalf("Prepare with no scope should fail before any request")
	}
}

// TestAPIErrorEnvelope asserts PowerContext's single error envelope decodes
// into Code/Message, so a caller can tell "the scope is not readable" (a
// configuration fact) from "the server is down" (a transient one).
func TestAPIErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "missing", nil) {
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"unknown scope"}}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Prepare(context.Background(), PrepareRequest{ScopeID: "missing"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusUnprocessableEntity || apiErr.Code != "invalid_request" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("a 422 is a configuration fact, not an outage")
	}
}

// TestUnavailable covers the transient path a pre-run inject degrades on.
func TestUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close()

	client, err := NewClient(Config{BaseURL: base})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Prepare(context.Background(), PrepareRequest{ScopeID: "s"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestRememberShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "multica:project:prj_1", nil) {
			return
		}
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		// Verbatim shape of a 1.6.0 answer: the body is `text`, and there is
		// no `content` field on a remembered record.
		_, _ = w.Write([]byte(`{"changed":true,"records":[{"kind":"decision","text":"handoff","state":"active","state_version":1,` +
			`"merged_into_id":null,"artifact":{"family":"atomic-memory","artifact_id":"mem_1","revision":1}}]}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.Remember(context.Background(), RememberRequest{
		ScopeID: "multica:project:prj_1",
		Kind:    "decision",
		Text:    "handoff for multica:issue:iss_1",
	})
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if gotPath != "/v1/memory/remember" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotBody["scope_id"] != testScopeID || gotBody["kind"] != "decision" {
		t.Fatalf("body = %v", gotBody)
	}
	if got.Changed != true || len(got.Records) != 1 || got.Records[0].Artifact.ArtifactID != "mem_1" {
		t.Fatalf("result = %+v", got)
	}
	if got.Records[0].Text != "handoff" {
		t.Fatalf("text = %q, want the entry body", got.Records[0].Text)
	}
}

func TestRememberRejectsEmptyText(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Remember(context.Background(), RememberRequest{ScopeID: "s", Text: "   "}); err == nil {
		t.Fatalf("Remember with no text should fail before any request")
	}
}

func TestSearchShape(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "multica:project:prj_1", nil) {
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		// Verbatim shape of a 1.6.0 search answer: hits carry the entry under
		// `memory`, and the entry body is `text`.
		_, _ = w.Write([]byte(`{"mode":"text","hits":[{"memory":{"kind":"decision","text":"already here","state":"active","state_version":0},` +
			`"score":0.5,"matched_by":["text"]}]}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.Search(context.Background(), SearchParams{ScopeID: "multica:project:prj_1", Query: "handoff iss_1", Limit: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotBody["scope_id"] != testScopeID || gotBody["query"] != "handoff iss_1" || gotBody["limit"] != float64(3) {
		t.Fatalf("body = %v", gotBody)
	}
	if texts := got.Texts(); len(texts) != 1 || texts[0] != "already here" {
		t.Fatalf("texts = %+v, want the hit body", texts)
	}
}

func TestNewClientRejectsBadBaseURL(t *testing.T) {
	for _, base := range []string{"", "   ", "127.0.0.1:8000", "ws://127.0.0.1:8000", "http://"} {
		if _, err := NewClient(Config{BaseURL: base}); err == nil {
			t.Fatalf("NewClient(%q) should fail", base)
		}
	}
}

// TestScopeForTask pins the mapping decision: project scope when there is a
// project, workspace scope as the fallback, and no scope at all when the task
// has neither.
func TestScopeForTask(t *testing.T) {
	cases := []struct {
		project, workspace string
		want               string
		ok                 bool
	}{
		{"prj_1", "ws_1", "multica:project:prj_1", true},
		{"", "ws_1", "multica:ws:ws_1", true},
		{"", "", "", false},
	}
	for _, tc := range cases {
		got, ok := ScopeForTask(tc.project, tc.workspace)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("ScopeForTask(%q,%q) = (%q,%v), want (%q,%v)", tc.project, tc.workspace, got, ok, tc.want, tc.ok)
		}
	}
	if ProjectScope("p") != "multica:project:p" || WorkspaceScope("w") != "multica:ws:w" {
		t.Fatalf("scope prefixes drifted from the D2 vocabulary")
	}
}

// TestResolveScopeMapsExternalReference is the contract a real 1.6.0 enforces:
// the ref is an external reference, the id is scp_..., and a route handed the
// ref answers scope_not_found. Resolution is cached across calls on one client.
func TestResolveScopeMapsExternalReference(t *testing.T) {
	var listings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serveScopes(w, r, "multica:project:prj_1", &listings) {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	for i := 0; i < 2; i++ {
		got, err := client.ResolveScope(context.Background(), "multica:project:prj_1")
		if err != nil {
			t.Fatalf("ResolveScope: %v", err)
		}
		if got != testScopeID {
			t.Fatalf("ResolveScope = %q, want %q", got, testScopeID)
		}
	}
	if listings.Load() != 1 {
		t.Fatalf("listings = %d, want 1 for a cached ref", listings.Load())
	}
}

// TestResolveScopeRejectsUnknownRef: a ref the server has no Scope for is a
// configuration fact, named locally, not a request that fails with a mystery
// scope_not_found on every write.
func TestResolveScopeRejectsUnknownRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[],"next_cursor":null}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.ResolveScope(context.Background(), "multica:ws:ws_1"); err == nil ||
		!strings.Contains(err.Error(), "multica:ws:ws_1") {
		t.Fatalf("err = %v, want it to name the unresolvable ref", err)
	}
}

// TestResolveScopePassesThroughWithoutListing keeps an older server (no
// GET /v1/scopes) working exactly as it did before resolution existed.
func TestResolveScopePassesThroughWithoutListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.ResolveScope(context.Background(), "multica:ws:ws_1")
	if err != nil || got != "multica:ws:ws_1" {
		t.Fatalf("ResolveScope = (%q, %v), want the ref passed through", got, err)
	}
}

// TestPrepareDecodesUnknownFieldsOnKnownShape guards the "ignore what we do not
// read" rule: the server's envelope carries fields the seam does not model, and
// a future addition must not break decoding.
func TestPrepareDecodesUnknownFieldsOnKnownShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "s", nil) {
			return
		}
		_, _ = w.Write([]byte(`{"schema":"prepare/2","status":"ready","content":"c","content_bytes":1,` +
			`"receipt":{"retrieval":{"searches":[]}},"future_field":true}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.Prepare(context.Background(), PrepareRequest{ScopeID: "s"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if strings.TrimSpace(got.Text()) != "c" {
		t.Fatalf("content = %q", got.Text())
	}
}

// resetScopeCacheForTest keeps the process-level cache from leaking between
// tests: every case uses its own httptest URL, but a shared TTL variable must
// also be restored.
func resetScopeCacheForTest(t *testing.T) {
	t.Helper()
	previousTTL := scopeCacheTTL
	scopeCache.Lock()
	scopeCache.entries = map[string]scopeCacheEntry{}
	scopeCache.Unlock()
	t.Cleanup(func() {
		scopeCache.Lock()
		scopeCache.entries = map[string]scopeCacheEntry{}
		scopeCache.Unlock()
		scopeCacheTTL = previousTTL
	})
}

// The daemon builds a new client on every claim. A per-client map alone still
// cost one GET /v1/scopes per run, which the shadow then priced as latency
// (M-625). Two different clients must share one process-level lookup — and a
// caller holding an already-resolved scp_ id must not trigger a second one.
func TestResolveScopeIsCachedAcrossClients(t *testing.T) {
	resetScopeCacheForTest(t)
	var listings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveScopes(w, r, "multica:project:prj_1", &listings) {
			return
		}
		_, _ = w.Write([]byte(`{"status":"empty","content":null,"content_bytes":0}`))
	}))
	defer srv.Close()

	for i := 0; i < 2; i++ {
		client, err := NewClient(Config{BaseURL: srv.URL})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		got, err := client.ResolveScope(context.Background(), "multica:project:prj_1")
		if err != nil || got != testScopeID {
			t.Fatalf("ResolveScope = (%q, %v), want %q", got, err, testScopeID)
		}
	}
	client, _ := NewClient(Config{BaseURL: srv.URL})
	if _, err := client.Prepare(context.Background(), PrepareRequest{ScopeID: testScopeID}); err != nil {
		t.Fatalf("Prepare with a resolved id: %v", err)
	}
	if listings.Load() != 1 {
		t.Fatalf("listings = %d, want 1 across clients and a resolved id", listings.Load())
	}
}

// A recreated Scope can change its generated id, so the process cache is
// bounded by a TTL. Once it expires the next client must look the ref up again.
func TestResolveScopeCacheExpires(t *testing.T) {
	resetScopeCacheForTest(t)
	scopeCacheTTL = 20 * time.Millisecond
	var listings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !serveScopes(w, r, "multica:ws:ws_1", &listings) {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	first, _ := NewClient(Config{BaseURL: srv.URL})
	if _, err := first.ResolveScope(context.Background(), "multica:ws:ws_1"); err != nil {
		t.Fatalf("first ResolveScope: %v", err)
	}
	if listings.Load() != 1 {
		t.Fatalf("listings = %d after the first lookup", listings.Load())
	}
	time.Sleep(40 * time.Millisecond)
	second, _ := NewClient(Config{BaseURL: srv.URL})
	if _, err := second.ResolveScope(context.Background(), "multica:ws:ws_1"); err != nil {
		t.Fatalf("second ResolveScope: %v", err)
	}
	if listings.Load() != 2 {
		t.Fatalf("listings = %d, want 2 after the TTL expired", listings.Load())
	}
}
