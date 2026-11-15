// Package moctapi implements the authenticated, workspace-scoped HTTP seam
// from multica-be to moct-be. moct-be trusts caller supplied scope, so this
// package constructs those values from the authenticated server identity.
package moctapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxBody = 64 << 10
	maxPage = 20
)

var (
	recordID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	uuid     = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Identity is derived by multica-be after workspace membership is checked.
type Identity struct {
	WorkspaceID      string
	ActorID          string
	Admin            bool
	AgentInWorkspace func(context.Context, string) (bool, error)
}

type Proxy struct {
	baseURL string
	client  *http.Client
}

func New(baseURL string) *Proxy {
	return &Proxy{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Serve routes one request. The caller must first authenticate the user and
// verify membership in Identity.WorkspaceID.
func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request, identity Identity, rest string) {
	if identity.WorkspaceID == "" || identity.ActorID == "" {
		writeError(w, http.StatusForbidden, "workspace access denied")
		return
	}
	if p.baseURL == "" || !safeOrigin(p.baseURL) {
		writeError(w, http.StatusServiceUnavailable, "moct-be is not configured")
		return
	}
	path := "/" + strings.TrimLeft(rest, "/")
	route, status := match(r.Method, path)
	if status != 0 {
		writeError(w, status, map[int]string{http.StatusNotFound: "not found", http.StatusMethodNotAllowed: "method not allowed"}[status])
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || r.Host == "" || u.Host != r.Host {
			writeError(w, http.StatusForbidden, "cross-origin request")
			return
		}
	}
	if r.Method == http.MethodPost && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
		return
	}
	if route.kind == "status" {
		p.status(w, r)
		return
	}
	query := url.Values{}
	query.Set("workspaceId", "multica:ws:"+identity.WorkspaceID)
	query.Set("expertId", "multica:user:"+identity.ActorID)
	query.Set("all", "1")
	var target string
	var body []byte
	method := r.Method
	switch route.kind {
	case "list":
		for _, key := range []string{"q", "types", "homes", "status", "cursor"} {
			if value := r.URL.Query().Get(key); value != "" {
				query.Set(key, truncate(value, map[string]int{"cursor": 256, "q": 256, "types": 256, "homes": 256, "status": 256}[key]))
			}
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil {
				writeError(w, 400, "limit must be a whole number")
				return
			}
			if n < 1 {
				n = 1
			}
			if n > maxPage {
				n = maxPage
			}
			query.Set("limit", strconv.Itoa(n))
		}
		expert, ok := p.expert(r.Context(), identity, r.URL.Query().Get("expertAgentId"), w)
		if !ok {
			return
		}
		query.Set("expertId", expert)
		target = "/team/v1/memory?" + query.Encode()
	case "usage":
		target = "/team/v1/memory/usage?" + url.Values{"workspaceId": {"multica:ws:" + identity.WorkspaceID}}.Encode()
	case "get":
		data, err := readBody(r)
		if err != nil {
			writeError(w, err.status, err.message)
			return
		}
		var input struct {
			IDs []string `json:"ids"`
		}
		if json.Unmarshal(data, &input) != nil || len(input.IDs) < 1 || len(input.IDs) > 10 {
			writeError(w, 400, "ids must be 1..10 record ids")
			return
		}
		for _, id := range input.IDs {
			if !recordID.MatchString(id) {
				writeError(w, 400, "ids must be 1..10 record ids")
				return
			}
		}
		expert, ok := p.expert(r.Context(), identity, r.URL.Query().Get("expertAgentId"), w)
		if !ok {
			return
		}
		query.Set("expertId", expert)
		body, _ = json.Marshal(input)
		method = http.MethodPost
		target = "/team/v1/memory/get?" + query.Encode()
	case "versions", "approve", "retract":
		found, wsID, err := p.lookup(r.Context(), identity, route.id)
		if err != nil {
			writeError(w, http.StatusBadGateway, "memory lookup failed")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "no such memory record in this workspace")
			return
		}
		if route.kind != "versions" {
			if wsID == "" {
				writeError(w, http.StatusForbidden, "a record outside any workspace cannot be approved or retracted here")
				return
			}
			if wsID != "multica:ws:"+identity.WorkspaceID {
				writeError(w, http.StatusNotFound, "no such memory record in this workspace")
				return
			}
			if !identity.Admin {
				writeError(w, http.StatusForbidden, "approving memory needs a workspace owner or admin")
				return
			}
			data, err := readBody(r)
			if err != nil {
				writeError(w, err.status, err.message)
				return
			}
			var input struct {
				BaseVersion int    `json:"baseVersion"`
				Reason      string `json:"reason"`
			}
			if json.Unmarshal(data, &input) != nil || input.BaseVersion < 1 {
				writeError(w, 400, "baseVersion must be the version you were shown")
				return
			}
			if len(input.Reason) > 500 {
				writeError(w, 400, "reason must be a string of at most 500 characters")
				return
			}
			payload := map[string]any{"baseVersion": input.BaseVersion, "actor": map[string]string{"kind": "human", "id": "multica:user:" + identity.ActorID}}
			if strings.TrimSpace(input.Reason) != "" {
				payload["reason"] = strings.TrimSpace(input.Reason)
			}
			body, _ = json.Marshal(payload)
		}
		method = http.MethodGet
		if route.kind != "versions" {
			method = http.MethodPost
		}
		target = "/team/v1/memory/" + url.PathEscape(route.id)
		if route.kind != "versions" {
			target += "/" + route.kind
		}
		if route.kind == "versions" {
			target += "/versions"
		}
	default:
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	p.relay(w, r, method, target, body)
}

type matchedRoute struct{ kind, id string }

func match(method, path string) (matchedRoute, int) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	get := method == http.MethodGet
	post := method == http.MethodPost
	if len(parts) == 1 && parts[0] == "status" {
		if get {
			return matchedRoute{kind: "status"}, 0
		}
		return matchedRoute{}, 405
	}
	if len(parts) == 3 && parts[0] == "team" && parts[1] == "v1" && parts[2] == "memory" {
		if get {
			return matchedRoute{kind: "list"}, 0
		}
		return matchedRoute{}, 404
	}
	if len(parts) == 4 && parts[0] == "team" && parts[1] == "v1" && parts[2] == "memory" {
		switch parts[3] {
		case "usage":
			if get {
				return matchedRoute{kind: "usage"}, 0
			}
			return matchedRoute{}, 405
		case "get":
			if post {
				return matchedRoute{kind: "get"}, 0
			}
			return matchedRoute{}, 405
		}
	}
	if len(parts) == 5 && parts[0] == "team" && parts[1] == "v1" && parts[2] == "memory" && recordID.MatchString(parts[3]) {
		switch parts[4] {
		case "versions":
			if get {
				return matchedRoute{kind: "versions", id: parts[3]}, 0
			}
			return matchedRoute{}, 405
		case "approve":
			if post {
				return matchedRoute{kind: "approve", id: parts[3]}, 0
			}
			return matchedRoute{}, 405
		case "retract":
			if post {
				return matchedRoute{kind: "retract", id: parts[3]}, 0
			}
			return matchedRoute{}, 405
		}
	}
	return matchedRoute{}, 404
}

func (p *Proxy) expert(ctx context.Context, identity Identity, requested string, w http.ResponseWriter) (string, bool) {
	if requested == "" {
		return "multica:user:" + identity.ActorID, true
	}
	if !uuid.MatchString(requested) {
		writeError(w, 400, "expertAgentId must be an agent id")
		return "", false
	}
	if identity.AgentInWorkspace == nil {
		writeError(w, 403, "agent is not in this workspace")
		return "", false
	}
	ok, err := identity.AgentInWorkspace(ctx, requested)
	if err != nil {
		writeError(w, 503, "multica did not answer the agent lookup")
		return "", false
	}
	if !ok {
		writeError(w, 403, "agent is not in this workspace")
		return "", false
	}
	return "multica:agent:" + requested, true
}

func (p *Proxy) lookup(ctx context.Context, identity Identity, id string) (bool, string, error) {
	q := url.Values{"workspaceId": {"multica:ws:" + identity.WorkspaceID}, "expertId": {"multica:user:" + identity.ActorID}, "all": {"1"}}
	body, _ := json.Marshal(map[string][]string{"ids": {id}})
	resp, err := p.request(ctx, http.MethodPost, "/team/v1/memory/get?"+q.Encode(), body)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, "", nil
	}
	var out struct {
		Records []struct {
			ID          string  `json:"id"`
			WorkspaceID *string `json:"workspaceId"`
		} `json:"records"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out) != nil || len(out.Records) == 0 || out.Records[0].ID != id {
		return false, "", nil
	}
	ws := ""
	if out.Records[0].WorkspaceID != nil {
		ws = *out.Records[0].WorkspaceID
	}
	return ws == "multica:ws:"+identity.WorkspaceID || ws == "", ws, nil
}

func (p *Proxy) status(w http.ResponseWriter, r *http.Request) {
	resp, err := p.request(r.Context(), http.MethodGet, "/team/v1/health", nil)
	reachable := err == nil && resp.StatusCode != 504
	schema := any(nil)
	ok := false
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			var payload struct {
				OK     bool `json:"ok"`
				Schema int  `json:"schema"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload) == nil {
				ok = payload.OK
				if payload.Schema > 0 {
					schema = payload.Schema
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"ok": ok, "schema": schema, "reachable": reachable})
}
func (p *Proxy) relay(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
	resp, err := p.request(r.Context(), method, path, body)
	if err != nil {
		writeError(w, 502, "moct-be is unreachable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		writeError(w, 502, "moct-be answered a redirect")
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		writeError(w, 502, "moct proxy failure")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}
func (p *Proxy) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return p.client.Do(req)
}

type requestError struct {
	status  int
	message string
}

func readBody(r *http.Request) ([]byte, *requestError) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, &requestError{400, "invalid request body"}
	}
	if len(b) > maxBody {
		return nil, &requestError{413, "body too large"}
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return []byte("{}"), nil
	}
	var x map[string]json.RawMessage
	if json.Unmarshal(b, &x) != nil || x == nil {
		return nil, &requestError{400, "invalid request body"}
	}
	return b, nil
}
func safeOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := strings.Trim(u.Hostname(), "[]")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
