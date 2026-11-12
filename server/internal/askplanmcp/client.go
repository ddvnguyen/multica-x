package askplanmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIClient is the thin HTTP client in front of multica-be's S1-A1 ask/plan
// routes (server/internal/handler/askplan.go). It authenticates with the
// task-scoped mat_ token exactly like every other agent-side CLI call: the
// server's middleware reads Authorization, stamps X-Task-ID authoritatively,
// and the handlers refuse anything but the caller's own running issue task.
//
// It never talks to MOCT — ask/plan is multica-be only (M-119 boundary).
type APIClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewAPIClient validates the two env-derived inputs and returns a client. A
// base URL that will not parse or an empty token is a configuration problem,
// reported once at startup (the caller serves anyway and fails each tool call
// with it) rather than by exiting: this process must outlive its own
// misconfiguration, because an MCP server that dies fails the whole ACP/dsh
// session (server/pkg/agent/acp_session.go).
func NewAPIClient(baseURL, token string) (*APIClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("multica-be base URL is empty (MULTICA_SERVER_URL)")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("multica-be base URL %q is not an http(s) URL", baseURL)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("task token is empty (MULTICA_TOKEN)")
	}
	return &APIClient{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		// 60s bounds one call like the memory tool server's: the ask park fans out inbox
		// items, which must not be cut off by a tight client timeout.
		http: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// do performs one JSON round trip and returns the HTTP status with the raw
// body for every non-transport outcome. Transport failures (server down, DNS,
// timeout) come back as err with status 0.
func (c *APIClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("multica-be is unreachable: %w", err)
	}
	defer resp.Body.Close()
	// 1 MiB caps a runaway the same way the memory tool server does; every real ask/plan
	// response is tiny (the largest is a fully written plan's sections).
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, payload, nil
}

// httpError renders a non-2xx answer as a tool error carrying the body
// VERBATIM: S1-A1 answers business outcomes with structured 409s whose body
// carries the current revision/status (e.g. "stale_plan_revision" with
// current_revision and current_status), and the agent needs those values
// intact to self-correct on the retry without a separate read action.
func httpError(status int, body []byte) error {
	text := strings.TrimSpace(string(body))
	if text == "" {
		text = "(empty body)"
	}
	return fmt.Errorf("multica-be refused the call: HTTP %d %s", status, text)
}
