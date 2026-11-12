// Package moct is the one place multica-x talks to moct-be (M-97 rule 4:
// every MOCT HTTP call lives here). moct-be stays the only writer to the
// memory store — this package is a pure HTTP client, never a DB handle.
//
// The ref strings this package builds and sends follow the D2 convention in
// docs/moct-seam.md: multica:ws:<uuid>, multica:agent:<uuid>,
// multica:issue:<uuid>, multica:project:<uuid>.
//
// Failure contract: every method returns an error instead of blocking or
// panicking. errors.Is(err, ErrUnavailable) reports "moct-be could not be
// reached" — callers that inject memory before a run log a warning and start
// the run without memory; memory never blocks a task.
package moct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultTimeout bounds a pre-run inject so a slow moct-be delays the
	// claim by at most this long before the caller degrades.
	defaultTimeout = 10 * time.Second
	// maxResponseBodySize caps moct-be answers; search results are small
	// (limit ≤ 20 records), so 1 MiB only trips on a runaway response.
	maxResponseBodySize = 1 << 20
	// teamPath is the moct-be HTTP prefix every seam endpoint lives under.
	teamPath = "/team/v1"
)

// Config constructs a Client. BaseURL is required (e.g. http://127.0.0.1:8110).
type Config struct {
	BaseURL    string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Client is a thin, concurrency-safe client for the moct-be team-memory HTTP
// surface. Zero value is not usable; construct with NewClient.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient validates the base URL once so a misconfigured lane fails at
// startup, not on the first pre-run inject.
func NewClient(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("moct: invalid base URL %q", cfg.BaseURL)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{baseURL: base, httpClient: httpClient}, nil
}

// --- read scope -----------------------------------------------------------

// Scope is the reader scope both read endpoints take as query params.
// WorkspaceID and ExpertID are required by moct-be; Channels and Tasks are
// OR-ed tag filters (see docs/moct-seam.md — scope tags widen, they do not
// narrow; inject is the per-issue narrowing path).
type Scope struct {
	WorkspaceID string   // multica:ws:<uuid>
	ExpertID    string   // multica:agent:<uuid>
	Channels    []string // e.g. multica:project:<uuid>
	Tasks       []string // e.g. multica:issue:<uuid>
	All         bool     // all=1: drop the tag filter, keep the workspace
}

func (s Scope) values() (url.Values, error) {
	if s.WorkspaceID == "" || s.ExpertID == "" {
		return nil, errors.New("moct: scope needs workspaceId and expertId")
	}
	q := url.Values{}
	q.Set("workspaceId", s.WorkspaceID)
	q.Set("expertId", s.ExpertID)
	if len(s.Channels) > 0 {
		q.Set("channels", strings.Join(s.Channels, ","))
	}
	if len(s.Tasks) > 0 {
		q.Set("tasks", strings.Join(s.Tasks, ","))
	}
	if s.All {
		q.Set("all", "1")
	}
	return q, nil
}

// --- endpoints ------------------------------------------------------------

// InjectParams are the query params of GET /team/v1/memory/inject — the
// pre-run path. TaskRef/ChannelRef are the D2 refs of the current issue and
// project; empty means that bucket is skipped.
type InjectParams struct {
	WorkspaceID string // required
	ExpertID    string // required
	TaskRef     string // multica:issue:<uuid>, optional
	ChannelRef  string // multica:project:<uuid>, optional
	LayerTokens int    // 0 = moct-be's configured inject budget
}

// Inject fetches the memory block to prepend to a task prompt. Read-only on
// moct-be; only active records come back.
func (c *Client) Inject(ctx context.Context, p InjectParams) (*Injection, error) {
	if p.WorkspaceID == "" || p.ExpertID == "" {
		return nil, errors.New("moct: inject needs workspaceId and expertId")
	}
	q := url.Values{}
	q.Set("workspaceId", p.WorkspaceID)
	q.Set("expertId", p.ExpertID)
	if p.TaskRef != "" {
		q.Set("taskRef", p.TaskRef)
	}
	if p.ChannelRef != "" {
		q.Set("channelRef", p.ChannelRef)
	}
	if p.LayerTokens > 0 {
		q.Set("layerTokens", strconv.Itoa(p.LayerTokens))
	}
	var out Injection
	if err := c.do(ctx, http.MethodGet, teamPath+"/memory/inject", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchParams are the query params of GET /team/v1/memory (memory_search).
type SearchParams struct {
	Scope
	Query  string   // q — every word must match title or body
	Types  []string // rule|decision|convention|how_to_work|task|branch
	Homes  []string // task|channel|team|expert
	Status string   // "", a status, or "all" ("" = moct-be default "active")
	Limit  int      // 1..20; 0 = moct-be default
	Cursor string   // offset cursor from a previous SearchResult.NextCursor
}

// Search runs memory_search against the scope. Scope tags are OR-ed: a
// record is visible when it matches workspaceId and any one tag — team
// records therefore show for every scope in their workspace.
func (c *Client) Search(ctx context.Context, p SearchParams) (*SearchResult, error) {
	q, err := p.Scope.values()
	if err != nil {
		return nil, err
	}
	if p.Query != "" {
		q.Set("q", p.Query)
	}
	if len(p.Types) > 0 {
		q.Set("types", strings.Join(p.Types, ","))
	}
	if len(p.Homes) > 0 {
		q.Set("homes", strings.Join(p.Homes, ","))
	}
	if p.Status != "" {
		q.Set("status", p.Status)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Cursor != "" {
		q.Set("cursor", p.Cursor)
	}
	var out SearchResult
	if err := c.do(ctx, http.MethodGet, teamPath+"/memory", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get fetches full records by id (memory_get). Ids the scope may not see and
// missing ids both land in GetResult.Missing, so a get never reveals that a
// record exists. 1..10 ids, per moct-be.
func (c *Client) Get(ctx context.Context, ids []string, scope Scope) (*GetResult, error) {
	if len(ids) == 0 || len(ids) > 10 {
		return nil, errors.New("moct: get needs 1..10 ids")
	}
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("moct: get ids must be non-empty")
		}
	}
	q, err := scope.values()
	if err != nil {
		return nil, err
	}
	body := struct {
		IDs []string `json:"ids"`
	}{IDs: ids}
	var out GetResult
	if err := c.do(ctx, http.MethodPost, teamPath+"/memory/get", q, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NoteParams is the body of POST /team/v1/memory (memory_note).
//
// There is deliberately no TaskRef field: moct-be rejects any taskRef that
// is not a task:… ref (live-verified, M-98), while D2 carries the issue as
// the task Home ref — see docs/moct-seam.md.
type NoteParams struct {
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Attrs       map[string]any `json:"attrs,omitempty"`
	Home        Home           `json:"home"`
	WorkspaceID string         `json:"workspaceId"`
	Actor       Actor          `json:"actor"`
	Pinned      bool           `json:"pinned,omitempty"`
	ConfirmNew  bool           `json:"confirmNew,omitempty"`
}

// Note writes one memory record. A 200 with Record == nil and NearMatches
// filled means moct-be held the write back for confirmation — resend with
// ConfirmNew to write anyway. Under moct-be's default expertApproval
// settings a worker's rule/decision/how_to_work lands status "pending" and
// does not inject until approved.
func (c *Client) Note(ctx context.Context, p NoteParams) (*NoteResult, error) {
	var out NoteResult
	if err := c.do(ctx, http.MethodPost, teamPath+"/memory", nil, p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- transport ------------------------------------------------------------

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("moct: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("moct: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Two %w: callers can errors.Is(err, ErrUnavailable) for the
		// degrade-with-a-warning path and still unwrap the transport error.
		return fmt.Errorf("moct: %s %s: %w: %w", method, path, err, ErrUnavailable)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize+1))
	if err != nil {
		return fmt.Errorf("moct: read %s %s: %w: %w", method, path, err, ErrUnavailable)
	}
	if len(data) > maxResponseBodySize {
		return fmt.Errorf("moct: %s %s: response exceeds %d bytes", method, path, maxResponseBodySize)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("moct: %s %s: decode response: %w", method, path, err)
		}
		return nil
	}
	return newAPIError(resp.StatusCode, data)
}

// newAPIError decodes moct-be's {ok:false,code,error} envelope; anything
// else (proxy HTML, empty body) still becomes an APIError carrying status.
func newAPIError(status int, body []byte) error {
	var envelope struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && (envelope.Code != "" || envelope.Error != "") {
		return &APIError{StatusCode: status, Code: envelope.Code, Message: envelope.Error}
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 256 {
		msg = msg[:256]
	}
	return &APIError{StatusCode: status, Message: msg}
}
