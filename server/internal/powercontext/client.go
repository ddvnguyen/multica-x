// Package powercontext is the one place multica-x talks to a PowerContext
// server (docs/powercontext-seam.md). It is a pure HTTP client: the server
// stays the only writer to its memory store, and nothing here opens a database
// handle or imports PowerContext's internals.
//
// It is the second implementation of the memory seam whose first is
// server/internal/moct, and it keeps that package's failure contract verbatim:
// every method returns an error instead of blocking or panicking, and
// errors.Is(err, ErrUnavailable) reports "the server could not be reached".
// Callers on the pre-run path log a warning and start the run without memory;
// memory never blocks a task.
//
// Scope vocabulary: the *external* ref strings stay Multica's D2 convention
// (multica:project:<uuid> and friends, server/internal/moct/refs.go). Because
// PowerContext has ONE flat Scope per read rather than moct-be's four buckets,
// the mapping happens here and only here — see refs.go.
package powercontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// defaultTimeout bounds a pre-run prepare so a slow server delays the
	// claim by at most this long before the caller degrades.
	defaultTimeout = 10 * time.Second
	// maxResponseBodySize caps answers. A prepared context is at most 32 KiB
	// plus a receipt that cannot exceed 8 KiB, so 1 MiB only trips on a
	// runaway response.
	maxResponseBodySize = 1 << 20
	// apiPath is the PowerContext HTTP prefix every route lives under.
	apiPath = "/v1"
)

// Bounds of the prepare byte budget, as the server enforces them.
const (
	MinMaxBytes     = 512
	MaxMaxBytes     = 32768
	DefaultMaxBytes = 8000
)

// Config constructs a Client. BaseURL is required (e.g. http://127.0.0.1:8000).
// Token is optional: the default loopback-only installation runs with
// authentication disabled, and an empty token then sends no Authorization
// header. A multi-user deployment must set it, but note that the server's
// static bearer represents one local administrator — per-tenant isolation is
// an Access-binding problem, not a token-suffix one.
type Config struct {
	BaseURL    string
	Token      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Client is a thin, concurrency-safe client for the PowerContext HTTP surface.
// Zero value is not usable; construct with NewClient.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client

	// scopes caches ref -> Scope id for this client's lifetime. A Scope id is
	// server-generated (scp_...), so a Multica ref has to be resolved once per
	// process instead of being sent as an id the server would not find.
	mu     sync.Mutex
	scopes map[string]string
}

// scopeCacheTTL bounds a process-level ref → Scope id mapping. Scope ids are
// generated when a Scope is created and do not move, so the TTL only limits how
// long a recreated Scope's stale id can survive. A variable so a test can
// exercise expiry without waiting minutes.
var scopeCacheTTL = 15 * time.Minute

type scopeCacheEntry struct {
	id        string
	expiresAt time.Time
}

// scopeCache is shared by every Client in the process. The daemon builds a new
// client on every claim, so a per-client map still paid one GET /v1/scopes per
// run — a cost the shadow's latency_ms then included (M-625). The base URL is
// part of the key: two PowerContext instances own different Scope ids for the
// same Multica ref.
var scopeCache = struct {
	sync.Mutex
	entries map[string]scopeCacheEntry
}{entries: map[string]scopeCacheEntry{}}

func scopeCacheKey(baseURL, ref string) string {
	return baseURL + "\x00" + strings.TrimSpace(ref)
}

func processCachedScope(baseURL, ref string) (string, bool) {
	key := scopeCacheKey(baseURL, ref)
	scopeCache.Lock()
	defer scopeCache.Unlock()
	entry, ok := scopeCache.entries[key]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		delete(scopeCache.entries, key)
		return "", false
	}
	return entry.id, true
}

func processRememberScope(baseURL, ref, id string) {
	key := scopeCacheKey(baseURL, ref)
	scopeCache.Lock()
	defer scopeCache.Unlock()
	scopeCache.entries[key] = scopeCacheEntry{id: id, expiresAt: time.Now().Add(scopeCacheTTL)}
}

// NewClient validates the base URL once so a misconfigured lane fails at
// startup, not on the first pre-run prepare.
func NewClient(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("powercontext: invalid base URL %q", cfg.BaseURL)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		baseURL:    base,
		token:      strings.TrimSpace(cfg.Token),
		httpClient: httpClient,
		scopes:     map[string]string{},
	}, nil
}

// ResolveScope maps a Multica ref (multica:project:<uuid>, multica:ws:<uuid>)
// to the Scope id the server's routes take.
//
// PowerContext generates Scope ids (scp_...) at creation and keeps the Multica
// ref as an external reference, so the ref is a lookup key, never an id: sent
// verbatim the server answers scope_not_found. Resolution is cached per client.
//
// A server that does not serve GET /v1/scopes at all (an older installation)
// gets the ref passed through unchanged, which is what those builds accepted.
func (c *Client) ResolveScope(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("powercontext: scope ref is empty")
	}
	if id, ok := c.cachedScope(ref); ok {
		return id, nil
	}
	if id, ok := processCachedScope(c.baseURL, ref); ok {
		c.rememberScope(ref, id)
		return id, nil
	}
	for _, field := range []string{"scope_id", "external_reference_value"} {
		page, err := c.listScopes(ctx, ref, field)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusMethodNotAllowed) {
				// No scope listing on this build: keep the pre-resolution contract.
				processRememberScope(c.baseURL, ref, ref)
				return ref, nil
			}
			return "", err
		}
		for _, scope := range page.Items {
			if scope.ScopeID == ref || hasExternalReference(scope, ref) {
				c.rememberScope(ref, scope.ScopeID)
				processRememberScope(c.baseURL, ref, scope.ScopeID)
				// Callers that already hold a resolved id pass it back into
				// Prepare; cache that key too so they never list a second time.
				processRememberScope(c.baseURL, scope.ScopeID, scope.ScopeID)
				return scope.ScopeID, nil
			}
		}
	}
	return "", fmt.Errorf("powercontext: no Scope carries the ref %q", ref)
}

func (c *Client) listScopes(ctx context.Context, query, field string) (*scopePage, error) {
	params := url.Values{"query": {query}, "query_field": {field}, "limit": {"50"}}
	var page scopePage
	if err := c.do(ctx, http.MethodGet, apiPath+"/scopes?"+params.Encode(), nil, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

func hasExternalReference(scope scopeDescriptor, ref string) bool {
	for _, ext := range scope.ExternalReferences {
		if ext.Value == ref {
			return true
		}
	}
	return false
}

func (c *Client) cachedScope(ref string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, ok := c.scopes[ref]
	return id, ok
}

func (c *Client) rememberScope(ref, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scopes == nil {
		c.scopes = map[string]string{}
	}
	c.scopes[ref] = id
}

// scopeID resolves the request's scope, so every route below accepts the D2
// ref vocabulary the callers speak (refs.go) rather than a raw Scope id.
func (c *Client) scopeID(ctx context.Context, ref string) (string, error) {
	return c.ResolveScope(ctx, ref)
}

// --- endpoints ------------------------------------------------------------

// Prepare assembles the bounded context text to prepend to a task prompt
// (POST /v1/context/prepare).
//
// MaxBytes is clamped into the server's accepted range rather than rejected: a
// caller handing over a budget it derived from a token count should degrade to
// the nearest legal byte budget, not lose the run's memory entirely.
func (c *Client) Prepare(ctx context.Context, req PrepareRequest) (*Prepared, error) {
	req.ScopeID = strings.TrimSpace(req.ScopeID)
	if req.ScopeID == "" {
		return nil, errors.New("powercontext: prepare needs a scope_id")
	}
	req.MaxBytes = ClampMaxBytes(req.MaxBytes)
	scope, err := c.scopeID(ctx, req.ScopeID)
	if err != nil {
		return nil, err
	}
	req.ScopeID = scope
	var out Prepared
	if err := c.do(ctx, http.MethodPost, apiPath+"/context/prepare", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Remember writes one curated memory entry (POST /v1/memory/remember). It is
// the post-run write path: a handoff or outcome lands as an entry the next
// prepare can recall.
func (c *Client) Remember(ctx context.Context, req RememberRequest) (*RememberResult, error) {
	req.ScopeID = strings.TrimSpace(req.ScopeID)
	if req.ScopeID == "" {
		return nil, errors.New("powercontext: remember needs a scope_id")
	}
	if strings.TrimSpace(req.Text) == "" {
		return nil, errors.New("powercontext: remember needs text")
	}
	scope, err := c.scopeID(ctx, req.ScopeID)
	if err != nil {
		return nil, err
	}
	req.ScopeID = scope
	var out RememberResult
	if err := c.do(ctx, http.MethodPost, apiPath+"/memory/remember", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchParams is POST /v1/memory/search: the recall path the seam uses to
// check whether a record is already there before writing it again.
type SearchParams struct {
	ScopeID string
	Query   string
	Limit   int
}

// SearchResult is POST /v1/memory/search's response: the ranking mode and the
// ranked hits. The entry text is SearchHit.Memory.Text — a decoder that looks
// for a top-level `results` array reads every search as empty.
type SearchResult struct {
	Mode string      `json:"mode"`
	Hits []SearchHit `json:"hits"`
}

// Texts returns the body of every hit, in rank order.
func (r *SearchResult) Texts() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Hits))
	for _, hit := range r.Hits {
		out = append(out, hit.Memory.Text)
	}
	return out
}

// Search runs a recall query in one scope.
func (c *Client) Search(ctx context.Context, p SearchParams) (*SearchResult, error) {
	scope := strings.TrimSpace(p.ScopeID)
	if scope == "" {
		return nil, errors.New("powercontext: search needs a scope_id")
	}
	resolved, err := c.scopeID(ctx, scope)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"scope_id": resolved}
	if q := strings.TrimSpace(p.Query); q != "" {
		body["query"] = q
	}
	if p.Limit > 0 {
		body["limit"] = p.Limit
	}
	var out SearchResult
	if err := c.do(ctx, http.MethodPost, apiPath+"/memory/search", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClampMaxBytes keeps a byte budget inside the range the server accepts.
func ClampMaxBytes(n int) int {
	if n <= 0 {
		return DefaultMaxBytes
	}
	if n < MinMaxBytes {
		return MinMaxBytes
	}
	if n > MaxMaxBytes {
		return MaxMaxBytes
	}
	return n
}

// --- transport ------------------------------------------------------------

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	target := c.baseURL + path

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("powercontext: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("powercontext: build %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Two %w: callers can errors.Is(err, ErrUnavailable) for the
		// degrade-with-a-warning path and still unwrap the transport error.
		return fmt.Errorf("powercontext: %s %s: %w: %w", method, path, err, ErrUnavailable)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize+1))
	if err != nil {
		return fmt.Errorf("powercontext: read %s %s: %w: %w", method, path, err, ErrUnavailable)
	}
	if len(data) > maxResponseBodySize {
		return fmt.Errorf("powercontext: %s %s: response exceeds %d bytes", method, path, maxResponseBodySize)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("powercontext: %s %s: decode response: %w", method, path, err)
		}
		return nil
	}
	return newAPIError(resp.StatusCode, data)
}

// newAPIError decodes PowerContext's {"error":{"code","message"}} envelope;
// anything else still becomes an APIError carrying the status.
func newAPIError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != "" {
		return &APIError{StatusCode: status, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 256 {
		msg = msg[:256]
	}
	return &APIError{StatusCode: status, Message: msg}
}
