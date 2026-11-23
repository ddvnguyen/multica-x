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
		{"get", "POST", "/team/v1/memory/get", `{"ids":["mem_1"]}`, "/team/v1/memory/get"},
		{"usage", "GET", "/team/v1/memory/usage", "", "/team/v1/memory/usage"},
		{"versions", "GET", "/team/v1/memory/mem_1/versions", "", "/team/v1/memory/mem_1/versions"},
		{"approve", "POST", "/team/v1/memory/mem_1/approve", `{"baseVersion":2}`, "/team/v1/memory/mem_1/approve"},
		{"retract", "POST", "/team/v1/memory/mem_1/retract", `{"baseVersion":2}`, "/team/v1/memory/mem_1/retract"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath := ""
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
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
