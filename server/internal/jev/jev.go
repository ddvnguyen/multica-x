// Package jev speaks TypeSafe's Jev ("System One") decision endpoint: one
// headless call that answers yes/no, choice or score questions about a state
// the caller sends, with no tools, no retries beyond one attempt and no policy.
//
// It exists because Multica's orchestration decisions are made by agent runs,
// not by a dsh host: `ctx.jev` (the @contexera/dsh-jev Cordis service) can
// never be reached from this process, so this package implements the same
// documented wire contract the service implements — `POST {apiBase}/systemone`
// with `{state, model, questions}` and `{model, answers, usage}` back. The body
// is byte-for-byte the one that package builds, which is what makes a shadow
// answer comparable to a live one later.
//
// Nothing here is on a request path. M-580 uses it for shadow mode only: the
// caller logs the answer next to the decision it already made and changes
// nothing. A failed call therefore never yields an answer and never an error a
// caller must handle — see Error.Kind.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The vendor's defaults, as the dsh service documents them.
const (
	// DefaultAPIBase is the vendor's own endpoint base; the call goes to this
	// plus "/systemone".
	DefaultAPIBase = "https://api.typesafe.ai/v1"
	// DefaultModel is a pinned version rather than an alias: a threshold is
	// calibrated against one model's answers, so a moving alias would
	// invalidate the calibration without anything failing.
	DefaultModel = "jev-1.13.0"
	// DefaultAPIKeyEnv is the variable the vendor's own examples use.
	DefaultAPIKeyEnv = "TYPESAFE_API_KEY"
	// DefaultTimeout bounds one call. Shadow mode must never outlive the
	// decision it observes, so this is deliberately shorter than the service's
	// own 30s default.
	DefaultTimeout = 1500 * time.Millisecond
	// Redacted replaces the API key in any text this package produces.
	Redacted = "[redacted]"

	systemOnePath = "/systemone"
	maxErrorBody  = 512
)

// ErrorKind says what a caller can do about a failure. A shadow caller can do
// nothing about any of them: it records the kind and carries on.
type ErrorKind string

const (
	// KindConfig means the endpoint, model or key is unusable. No call was
	// made.
	KindConfig ErrorKind = "config"
	// KindRequest means this call was malformed. Nothing was sent.
	KindRequest ErrorKind = "request"
	// KindTransport means the network or TLS failed.
	KindTransport ErrorKind = "transport"
	// KindTimeout means no answer arrived within the call's bound.
	KindTimeout ErrorKind = "timeout"
	// KindHTTP means the endpoint answered with a non-2xx status.
	KindHTTP ErrorKind = "http"
	// KindProtocol means a 2xx body that is not the documented response.
	KindProtocol ErrorKind = "protocol"
)

// Error is the one failure type this package raises. Its message never carries
// the API key. RetryAfter is the vendor's requested pause after a 429, zero when
// it sent no usable Retry-After header; callers decide what to do with it.
type Error struct {
	Kind       ErrorKind
	Status     int
	RetryAfter time.Duration
	msg        string
}

func (e *Error) Error() string { return e.msg }

// KindOf reports the kind of a failure, or "" when it is not a Jev failure.
func KindOf(err error) ErrorKind {
	var jevErr *Error
	if errors.As(err, &jevErr) {
		return jevErr.Kind
	}
	return ""
}

// RetryAfterOf reports the pause the endpoint asked for after a 429, or zero
// when the failure was not a rate limit or carried no usable Retry-After. It is
// kept beside KindOf so callers need not reach into this package's Error type.
func RetryAfterOf(err error) time.Duration {
	var jevErr *Error
	if errors.As(err, &jevErr) {
		return jevErr.RetryAfter
	}
	return 0
}

// IsRateLimited reports whether the endpoint answered 429. Shadow callers use
// it to record a distinct error kind and to open a circuit breaker.
func IsRateLimited(err error) bool {
	var jevErr *Error
	return errors.As(err, &jevErr) && jevErr.Kind == KindHTTP && jevErr.Status == http.StatusTooManyRequests
}

// retryAfter parses an HTTP Retry-After header, in either seconds or HTTP-date
// form. A missing or unusable value is zero; the caller picks its own default.
func retryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		if pause := at.Sub(now); pause > 0 {
			return pause
		}
	}
	return 0
}

// Question is one typed question: `noul` (yes/no) or `choice` (one option out
// of a fixed set). `score` is a later change, not an untested guess. Shapes
// match `@contexera/dsh-jev` contracts: a choice carries its options as the
// option map, noul its true/false entries, and the choice answer is the
// chosen key plus the full distribution.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// NoulCriteria names what a yes and a no mean. Either side may be omitted. A
// struct rather than a map so the wire keeps the documented key order.
type NoulCriteria struct {
	True  string `json:"true,omitempty"`
	False string `json:"false,omitempty"`
}

// Noul is the yes/no question the shadow asks.
func Noul(instructions, yes, no string) Question {
	return Question{Type: "noul", Instructions: instructions, Criteria: NoulCriteria{True: yes, False: no}}
}

// Choice is the routing question the shadow asks: pick one option out of the
// fixed set. Options maps each option name to its description; it must offer
// at least one option, mirroring the dsh package's readChoiceCriteria.
func Choice(instructions string, options map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: options}
}

// Answer is the vendor's answer, passed through as sent. It declares the
// members a `noul` or `choice` answer carries; an answer of another type still
// unmarshals, with the fields it does not use left zero.
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

// Result is the vendor's response. Model reports the versioned id that
// answered, which is what makes a pinned model verifiable; Usage is the
// vendor's accounting record, kept unmodified.
type Result struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   map[string]any    `json:"usage"`
}

// Config is everything a deployment may vary. The key is read from the
// environment; it never enters a configuration file or a log line.
type Config struct {
	APIBase string
	Model   string
	APIKey  string
	Timeout time.Duration
}

// ConfigFromEnv reads JEV_API_BASE, JEV_MODEL, JEV_TIMEOUT_MS and the key.
// The key is JEV_API_KEY when set, otherwise TYPESAFE_API_KEY — the variable
// the vendor's examples and the dsh service use. ok is false when no key is
// resolvable, which is the only thing that turns the client off.
func ConfigFromEnv(getenv func(string) string) (cfg Config, ok bool) {
	key := strings.TrimSpace(getenv("JEV_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(getenv(DefaultAPIKeyEnv))
	}
	if key == "" {
		return Config{}, false
	}
	cfg = Config{APIBase: DefaultAPIBase, Model: DefaultModel, APIKey: key, Timeout: DefaultTimeout}
	if base := strings.TrimSpace(getenv("JEV_API_BASE")); base != "" {
		cfg.APIBase = base
	}
	if model := strings.TrimSpace(getenv("JEV_MODEL")); model != "" {
		cfg.Model = model
	}
	if raw := strings.TrimSpace(getenv("JEV_TIMEOUT_MS")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			cfg.Timeout = time.Duration(ms) * time.Millisecond
		}
	}
	return cfg, true
}

// Client makes one decision call at a time. It is safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient resolves the config's defaults and checks what can be checked
// without a call. A bad endpoint or a missing key fails here, once, rather than
// on every dispatch.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, &Error{Kind: KindConfig, msg: "jev: no API key configured"}
	}
	base := strings.TrimSpace(cfg.APIBase)
	if base == "" {
		base = DefaultAPIBase
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, &Error{Kind: KindConfig, msg: "jev: apiBase must be an absolute http(s) URL"}
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cfg.APIBase = strings.TrimRight(base, "/")
	cfg.Model = model
	cfg.Timeout = timeout
	return &Client{cfg: cfg, http: &http.Client{}}, nil
}

// Redact removes the key from text a caller is about to log. Every message this
// package produces is already redacted; a caller that adds context of its own
// can run it through here.
func (c *Client) Redact(text string) string {
	if c == nil || c.cfg.APIKey == "" {
		return text
	}
	return strings.ReplaceAll(text, c.cfg.APIKey, Redacted)
}

// Decide asks one set of questions about one state and returns the vendor's
// answers. It makes exactly one attempt: a shadow observation that retries
// would spend more of the decision's budget than the observation is worth,
// and a missed row is cheaper than a dispatch slowed down.
func (c *Client) Decide(ctx context.Context, state any, questions map[string]Question) (*Result, error) {
	if len(questions) == 0 {
		return nil, &Error{Kind: KindRequest, msg: "jev: at least one question is required"}
	}
	for id, q := range questions {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(q.Instructions) == "" {
			return nil, &Error{Kind: KindRequest, msg: "jev: every question needs an id and instructions"}
		}
		switch q.Type {
		case "noul":
		case "choice":
			options, ok := q.Criteria.(map[string]string)
			if !ok || len(options) == 0 {
				return nil, &Error{Kind: KindRequest, msg: fmt.Sprintf("jev: choice question %q must offer at least one option", id)}
			}
		default:
			return nil, &Error{Kind: KindRequest, msg: fmt.Sprintf("jev: unsupported question type %q", q.Type)}
		}
	}
	body, err := json.Marshal(map[string]any{"state": state, "model": c.cfg.Model, "questions": questions})
	if err != nil {
		return nil, &Error{Kind: KindRequest, msg: "jev: could not encode the request: " + c.Redact(err.Error())}
	}

	callCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.cfg.APIBase+systemOnePath, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindConfig, msg: "jev: could not build the request: " + c.Redact(err.Error())}
	}
	req.Header.Set("authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, &Error{Kind: KindTimeout, msg: fmt.Sprintf("jev: no answer within %s", c.cfg.Timeout)}
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, &Error{Kind: KindTimeout, msg: fmt.Sprintf("jev: no answer within %s", c.cfg.Timeout)}
		}
		return nil, &Error{Kind: KindTransport, msg: "jev: transport failed: " + c.Redact(err.Error())}
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, &Error{Kind: KindTransport, msg: "jev: could not read the answer: " + c.Redact(readErr.Error())}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{
			Kind:       KindHTTP,
			Status:     resp.StatusCode,
			RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now()),
			msg:        fmt.Sprintf("jev: endpoint answered %d: %s", resp.StatusCode, c.Redact(snippet(raw))),
		}
	}

	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, &Error{Kind: KindProtocol, msg: "jev: response is not the documented envelope: " + c.Redact(snippet(raw))}
	}
	if result.Model == "" {
		return nil, &Error{Kind: KindProtocol, msg: "jev: response carries no model id"}
	}
	if len(result.Answers) != len(questions) {
		return nil, &Error{Kind: KindProtocol, msg: fmt.Sprintf("jev: %d answers for %d questions", len(result.Answers), len(questions))}
	}
	for id := range questions {
		if _, ok := result.Answers[id]; !ok {
			return nil, &Error{Kind: KindProtocol, msg: fmt.Sprintf("jev: no answer for question %q", id)}
		}
	}
	return &result, nil
}

// snippet bounds what an error message quotes of a body, because an error
// message is not a place to paste a response.
func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > maxErrorBody {
		return text[:maxErrorBody] + "..."
	}
	return text
}
