package moctapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAllowlistedRoutesForwardScopedRequests(t *testing.T) {
	tests := []struct {
		name, method, path, body, upstreamPath string
	}{
		{"list", "GET", "/team/v1/memory?limit=8", "", "/team/v1/memory"},
		{"create", "POST", "/team/v1/memory", `{"type":"rule","title":"t","body":"b","attrs":{"level":"must"}}`, "/team/v1/memory"},
		{"get", "POST", "/team/v1/memory/get", `{"ids":["mem_1"]}`, "/team/v1/memory/get"},
		{"usage", "GET", "/team/v1/memory/usage", "", "/team/v1/memory/usage"},
		{"versions", "GET", "/team/v1/memory/mem_1/versions", "", "/team/v1/memory/mem_1/versions"},
		{"approve", "POST", "/team/v1/memory/mem_1/approve", `{"baseVersion":2}`, "/team/v1/memory/mem_1/approve"},
		{"retract", "POST", "/team/v1/memory/mem_1/retract", `{"baseVersion":2}`, "/team/v1/memory/mem_1/retract"},
		{"purge", "POST", "/team/v1/memory/mem_1/purge", `{}`, "/team/v1/memory/mem_1/purge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath := ""
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				// M-546: single-record routes are scoped by workspace — the
				// relay carries the same scope the lookup gate just proved.
				if strings.HasPrefix(r.URL.Path, "/team/v1/memory/mem_") {
					if got := r.URL.Query().Get("workspaceId"); got != "multica:ws:ws-a" {
						t.Errorf("%s workspace scope = %q, want multica:ws:ws-a", r.URL.Path, got)
					}
				}
				if r.URL.Path == "/team/v1/memory/get" && tc.name != "get" {
					_, _ = io.WriteString(w, `{"records":[{"id":"mem_1","workspaceId":"multica:ws:ws-a"}]}`)
					return
				}
				if r.URL.Path == "/team/v1/memory/get" && tc.name == "get" {
					if r.URL.Query().Get("workspaceId") != "multica:ws:ws-a" {
						t.Errorf("workspace scope = %q", r.URL.Query().Get("workspaceId"))
					}
					if r.URL.Query().Get("expertId") != "multica:user:user-a" {
						t.Errorf("actor scope = %q", r.URL.Query().Get("expertId"))
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer backend.Close()
			proxy := New(backend.URL)
			req := httptest.NewRequest(tc.method, "http://app.test"+tc.path, strings.NewReader(tc.body))
			if tc.method == "POST" {
				req.Header.Set("Content-Type", "application/json")
			}
			res := httptest.NewRecorder()
			proxy.Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a", Admin: true}, strings.SplitN(tc.path, "?", 2)[0])
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d body %s", res.Code, res.Body.String())
			}
			if gotPath != tc.upstreamPath {
				t.Fatalf("upstream path = %q, want %q", gotPath, tc.upstreamPath)
			}
		})
	}
}

func TestStatusIsHostAuthoredAndDoesNotExposeBackendDetails(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/v1/health" {
			t.Fatalf("health path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"ok":true,"schema":8,"dbPath":"/private/path"}`)
	}))
	defer backend.Close()
	req := httptest.NewRequest("GET", "http://app.test/status", nil)
	res := httptest.NewRecorder()
	New(backend.URL).Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a"}, "/status")
	var body map[string]any
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil {
		t.Fatalf("status/body = %d %s", res.Code, res.Body.String())
	}
	if body["ok"] != true || body["schema"] != float64(8) || body["reachable"] != true || strings.Contains(res.Body.String(), "dbPath") {
		t.Fatalf("status response = %s", res.Body.String())
	}
}

func TestApproveRequiresAdminAndRejectsForeignWorkspaceRecord(t *testing.T) {
	for _, tc := range []struct {
		name            string
		admin           bool
		recordWorkspace string
		want            int
	}{
		{"member denied", false, "multica:ws:ws-a", http.StatusForbidden},
		{"foreign workspace hidden", true, "multica:ws:ws-b", http.StatusNotFound},
		{"admin allowed", true, "multica:ws:ws-a", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/team/v1/memory/get" {
					_, _ = io.WriteString(w, `{"records":[{"id":"mem_1","workspaceId":"`+tc.recordWorkspace+`"}]}`)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer backend.Close()
			req := httptest.NewRequest("POST", "http://app.test/team/v1/memory/mem_1/approve", strings.NewReader(`{"baseVersion":1}`))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			New(backend.URL).Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a", Admin: tc.admin}, "/team/v1/memory/mem_1/approve")
			if res.Code != tc.want {
				t.Fatalf("status = %d body %s, want %d", res.Code, res.Body.String(), tc.want)
			}
		})
	}
}

// M-570: create-note and purge are owner/admin-only additions to the closed
// allow-list. The proxy authors actor + workspace scope server-side; purge
// additionally re-checks the record's workspace through a lookup.
func TestCreateNoteNeedsAdminAndAuthorsScope(t *testing.T) {
	var gotBody map[string]any
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/team/v1/memory" || r.Method != http.MethodPost {
			t.Errorf("upstream = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if r.URL.Query().Get("workspaceId") != "multica:ws:ws-a" {
			t.Errorf("workspace scope = %q", r.URL.Query().Get("workspaceId"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"record":{"id":"mem_9","status":"active"}}`)
	}))
	defer backend.Close()
	proxy := New(backend.URL)
	request := func(admin bool, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://app.test/team/v1/memory", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		proxy.Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a", Admin: admin}, "/team/v1/memory")
		return res
	}
	if res := request(false, `{"type":"decision","title":"t","body":"b"}`); res.Code != http.StatusForbidden {
		t.Fatalf("member create status = %d body %s, want 403", res.Code, res.Body.String())
	}
	for _, tc := range []struct{ name, body string }{
		{"missing title", `{"type":"decision","body":"b"}`},
		{"bad type", `{"type":"task","title":"t","body":"b"}`},
		{"foreign team home", `{"type":"decision","title":"t","body":"b","homeKind":"team","homeRef":"multica:ws:other"}`},
		{"decision needs issue home", `{"type":"decision","title":"t","body":"b","homeKind":"team","homeRef":"multica:ws:ws-a"}`},
		{"decision needs attrs", `{"type":"decision","title":"t","body":"b","homeKind":"task","homeRef":"multica:issue:22222222-2222-4222-8222-222222222222"}`},
		{"rule needs level", `{"type":"rule","title":"t","body":"b","homeKind":"team","homeRef":"multica:ws:ws-a"}`},
		{"how_to_work not in workspace", `{"type":"how_to_work","title":"t","body":"b","homeKind":"team","homeRef":"multica:ws:ws-a"}`},
	} {
		if res := request(true, tc.body); res.Code != http.StatusBadRequest && res.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d body %s, want 4xx", tc.name, res.Code, res.Body.String())
		}
	}
	// A rule on the workspace with its level is the allowed shape: the proxy
	// defaults an empty home to the team home, and authors actor + scope.
	res := request(true, `{"type":"rule","title":"t","body":"b","attrs":{"level":"must"}}`)
	if res.Code != http.StatusOK {
		t.Fatalf("admin create status = %d body %s", res.Code, res.Body.String())
	}
	actor, _ := gotBody["actor"].(map[string]any)
	if gotBody["workspaceId"] != "multica:ws:ws-a" || actor["kind"] != "human" || actor["id"] != "multica:user:user-a" {
		t.Fatalf("server-authored scope = %v", gotBody)
	}
	home, _ := gotBody["home"].(map[string]any)
	if home["kind"] != "team" || home["ref"] != "multica:ws:ws-a" {
		t.Fatalf("default home = %v", home)
	}
	attrs, _ := gotBody["attrs"].(map[string]any)
	if attrs["level"] != "must" {
		t.Fatalf("attrs = %v, want level must", gotBody["attrs"])
	}
}

func TestPurgeNeedsAdminAndOwnWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name            string
		admin           bool
		recordWorkspace string
		want            int
	}{
		{"member denied", false, "multica:ws:ws-a", http.StatusForbidden},
		{"foreign workspace hidden", true, "multica:ws:ws-b", http.StatusNotFound},
		{"admin allowed", true, "multica:ws:ws-a", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotActor map[string]any
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/team/v1/memory/get" {
					_, _ = io.WriteString(w, `{"records":[{"id":"mem_1","workspaceId":"`+tc.recordWorkspace+`"}]}`)
					return
				}
				if r.URL.Path != "/team/v1/memory/mem_1/purge" || r.Method != http.MethodPost {
					t.Errorf("upstream = %s %s", r.Method, r.URL.Path)
				}
				var payload struct {
					Actor map[string]any `json:"actor"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				gotActor = payload.Actor
				_, _ = io.WriteString(w, `{"id":"mem_1","status":"purged"}`)
			}))
			defer backend.Close()
			req := httptest.NewRequest("POST", "http://app.test/team/v1/memory/mem_1/purge", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			New(backend.URL).Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a", Admin: tc.admin}, "/team/v1/memory/mem_1/purge")
			if res.Code != tc.want {
				t.Fatalf("status = %d body %s, want %d", res.Code, res.Body.String(), tc.want)
			}
			if tc.want == http.StatusOK && (gotActor["kind"] != "human" || gotActor["id"] != "multica:user:user-a") {
				t.Fatalf("purge actor = %v, want the human caller", gotActor)
			}
		})
	}
}

func TestMoctSecretEnvelopeRelaysByteForByte(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"ok":false,"code":"SECRET_DETECTED","error":"content looks like a credential"}`)
	}))
	defer backend.Close()
	proxy := New(backend.URL)
	// A rule on the team home with its level is the allowed shape that reaches
	// moct-be: the proxy validates the home, then relays the backend's answer.
	body := `{"type":"rule","title":"t","body":"b","homeKind":"team","homeRef":"multica:ws:ws-a","attrs":{"level":"must"}}`
	req := httptest.NewRequest("POST", "http://app.test/team/v1/memory", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	proxy.Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a", Admin: true}, "/team/v1/memory")
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("secret status = %d body %s, want 422", res.Code, res.Body.String())
	}
	var errorBody map[string]any
	if json.Unmarshal(res.Body.Bytes(), &errorBody) != nil || errorBody["code"] != "SECRET_DETECTED" {
		t.Fatalf("secret body = %s", res.Body.String())
	}
}

func TestMoctErrorsHaveStableShapeAndAllowlistIsClosed(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"ok":false,"code":"bad","error":"invalid"}`)
	}))
	defer backend.Close()
	proxy := New(backend.URL)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://app.test"+path, strings.NewReader(body))
		if method == "POST" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		proxy.Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a", Admin: true}, path)
		return res
	}
	bad := request("GET", "/team/v1/memory/mem_1/restore", "")
	if bad.Code != http.StatusNotFound {
		t.Fatalf("closed allowlist status = %d", bad.Code)
	}
	var errorBody map[string]any
	if json.Unmarshal(bad.Body.Bytes(), &errorBody) != nil || errorBody["ok"] != false || errorBody["error"] != "not found" {
		t.Fatalf("error body = %s", bad.Body.String())
	}
	relayed := request("GET", "/team/v1/memory", "")
	if relayed.Code != http.StatusUnprocessableEntity {
		t.Fatalf("upstream status = %d", relayed.Code)
	}
}

func TestListIgnoresCallerWorkspaceAndBoundsLimit(t *testing.T) {
	var got url.Values
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Query(); _, _ = io.WriteString(w, `{}`) }))
	defer backend.Close()
	req := httptest.NewRequest("GET", "http://app.test/team/v1/memory?workspaceId=other&expertId=other&limit=999", nil)
	res := httptest.NewRecorder()
	New(backend.URL).Serve(res, req, Identity{WorkspaceID: "ws-a", ActorID: "user-a"}, "/team/v1/memory")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", res.Code, res.Body.String())
	}
	if got.Get("workspaceId") != "multica:ws:ws-a" || got.Get("expertId") != "multica:user:user-a" || got.Get("limit") != "20" {
		t.Fatalf("forwarded query = %v", got)
	}
}

// M-542: null-workspace expert-home records are Role knowledge containment.
// A home expert that is a Role HERE owns the knowledge here (visible);
// a home expert that lives nowhere this workspace can see stays hidden.
// Every other shape keeps the legacy allowance.
func TestVersionsContainsNullWorkspaceRoleKnowledge(t *testing.T) {
	const (
		roleHereID   = "55555555-5555-4555-8555-555555555555"
		agentHereID  = "66666666-6666-4666-8666-666666666666"
		foreignID    = "77777777-7777-4777-8777-777777777777"
		versionsPath = "/team/v1/memory/mem_r/versions"
	)
	for _, tc := range []struct {
		name     string
		record   string
		wantCode int
	}{
		{"role in this workspace visible", `{"id":"mem_r","home":{"kind":"expert","ref":"multica:agent:` + roleHereID + `"}}`, http.StatusOK},
		{"agent in this workspace keeps legacy", `{"id":"mem_r","home":{"kind":"expert","ref":"multica:agent:` + agentHereID + `"}}`, http.StatusOK},
		{"expert from another workspace hidden", `{"id":"mem_r","home":{"kind":"expert","ref":"multica:agent:` + foreignID + `"}}`, http.StatusNotFound},
		{"team home keeps legacy", `{"id":"mem_r","home":{"kind":"team","ref":"multica:ws:ws-a"}}`, http.StatusOK},
		{"workspace-set record still exact-matched", `{"id":"mem_r","workspaceId":"multica:ws:ws-a","home":{"kind":"expert","ref":"multica:agent:` + roleHereID + `"}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/team/v1/memory/get" {
					_, _ = io.WriteString(w, `{"records":[`+tc.record+`]}`)
					return
				}
				_, _ = io.WriteString(w, `{"versions":[]}`)
			}))
			defer backend.Close()
			identity := Identity{WorkspaceID: "ws-a", ActorID: "user-a", Admin: true}
			identity.AgentInWorkspace = func(_ context.Context, id string) (bool, error) {
				return id == roleHereID || id == agentHereID, nil
			}
			identity.RoleInWorkspace = func(_ context.Context, id string) (bool, error) {
				return id == roleHereID, nil
			}
			req := httptest.NewRequest("GET", "http://app.test"+versionsPath, nil)
			res := httptest.NewRecorder()
			New(backend.URL).Serve(res, req, identity, versionsPath)
			if res.Code != tc.wantCode {
				t.Fatalf("status = %d body %s, want %d", res.Code, res.Body.String(), tc.wantCode)
			}
		})
	}
}
