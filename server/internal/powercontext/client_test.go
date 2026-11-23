package powercontext

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPrepareSendsContract pins the wire contract the seam depends on: the
// route, the four fields that drive recall, and the bearer header when a token
// is configured. A refactor that renames a JSON field here silently empties
// every pre-run block, so the shape is asserted rather than inferred.
func TestPrepareSendsContract(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if gotBody["scope_id"] != "multica:project:prj_1" {
		t.Fatalf("scope_id = %v", gotBody["scope_id"])
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
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"records":[{"content":"handoff","state":"active","state_version":1,` +
			`"artifact":{"family":"atomic-memory","artifact_id":"mem_1","revision":1}}]}`))
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
	if gotBody["scope_id"] != "multica:project:prj_1" || gotBody["kind"] != "decision" {
		t.Fatalf("body = %v", gotBody)
	}
	if len(got.Records) != 1 || got.Records[0].Artifact.ArtifactID != "mem_1" {
		t.Fatalf("records = %+v", got.Records)
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
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"results":[{"content":"already here"}]}`))
	}))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.Search(context.Background(), SearchParams{ScopeID: "s", Query: "handoff iss_1", Limit: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotBody["scope_id"] != "s" || gotBody["query"] != "handoff iss_1" || gotBody["limit"] != float64(3) {
		t.Fatalf("body = %v", gotBody)
	}
	if len(got.Results) != 1 {
		t.Fatalf("results = %+v", got.Results)
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

// TestPrepareDecodesUnknownFieldsOnKnownShape guards the "ignore what we do not
// read" rule: the server's envelope carries fields the seam does not model, and
// a future addition must not break decoding.
func TestPrepareDecodesUnknownFieldsOnKnownShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
