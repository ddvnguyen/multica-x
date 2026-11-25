package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "sk-jev-test-key-9f3a"

func client(t *testing.T, base string, timeout time.Duration) *Client {
	t.Helper()
	c, err := NewClient(Config{APIBase: base, APIKey: testKey, Timeout: timeout})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func answersBody(t *testing.T, noul float64) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":   DefaultModel,
		"answers": map[string]any{"needs_action": map[string]any{"type": "noul", "noul": noul}},
		"usage":   map[string]any{"input_tokens": 231, "output_tokens": 9},
	})
	if err != nil {
		t.Fatalf("encode stub body: %v", err)
	}
	return string(body)
}

// The body this package sends must be the one the dsh service builds, because a
// shadow answer is only comparable to a live one if the question was the same
// shape. The expectation is a literal, not a re-encoding of the input.
func TestDecideSendsTheDocumentedBody(t *testing.T) {
	var gotBody, gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody, gotAuth, gotPath = string(raw), r.Header.Get("authorization"), r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, answersBody(t, 0.81))
	}))
	defer srv.Close()

	state := map[string]any{"issue": map[string]any{"status": "in_progress"}}
	question := Noul("will the run act?", "it acts", "it does not")
	if _, err := client(t, srv.URL, time.Second).Decide(context.Background(), state, map[string]Question{"needs_action": question}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	if gotPath != "/systemone" {
		t.Errorf("path = %q, want /systemone", gotPath)
	}
	if gotAuth != "Bearer "+testKey {
		t.Errorf("authorization = %q, want the bearer key", gotAuth)
	}
	want := `{"model":"jev-1.13.0","questions":{"needs_action":{"type":"noul","instructions":"will the run act?","criteria":{"true":"it acts","false":"it does not"}}},"state":{"issue":{"status":"in_progress"}}}`
	if gotBody != want {
		t.Errorf("body mismatch\n got: %s\nwant: %s", gotBody, want)
	}
}

func TestDecideReturnsTheVendorsAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, answersBody(t, 0.23))
	}))
	defer srv.Close()

	result, err := client(t, srv.URL, time.Second).Decide(context.Background(), "state", map[string]Question{
		"needs_action": Noul("q", "y", "n"),
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if result.Model != DefaultModel {
		t.Errorf("model = %q, want %q", result.Model, DefaultModel)
	}
	if got := result.Answers["needs_action"].Noul; got != 0.23 {
		t.Errorf("noul = %v, want 0.23", got)
	}
	if got := result.Usage["input_tokens"]; got != float64(231) {
		t.Errorf("usage input_tokens = %v, want 231", got)
	}
}

func TestDecideClassifiesFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    ErrorKind
	}{
		{
			name:    "http status",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			want:    KindHTTP,
		},
		{
			name: "protocol: not json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "<html>nope</html>")
			},
			want: KindProtocol,
		},
		{
			name: "protocol: missing answer",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{}}`)
			},
			want: KindProtocol,
		},
		{
			name: "timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(150 * time.Millisecond)
			},
			want: KindTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			_, err := client(t, srv.URL, 50*time.Millisecond).Decide(context.Background(), "state", map[string]Question{
				"needs_action": Noul("q", "y", "n"),
			})
			if err == nil {
				t.Fatalf("want an error, got an answer")
			}
			if got := KindOf(err); got != tc.want {
				t.Fatalf("kind = %q, want %q (err: %v)", got, tc.want, err)
			}
		})
	}
}

func TestDecideTransportFailureIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	_, err := client(t, url, time.Second).Decide(context.Background(), "state", map[string]Question{
		"needs_action": Noul("q", "y", "n"),
	})
	if got := KindOf(err); got != KindTransport {
		t.Fatalf("kind = %q, want %q (err: %v)", got, KindTransport, err)
	}
}

// The key must never appear in a message, including one built from a body the
// endpoint echoed back — a key in a log is a key that has to be rotated.
func TestDecideRedactsTheKeyFromErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid key `+testKey+`"}`)
	}))
	defer srv.Close()

	_, err := client(t, srv.URL, time.Second).Decide(context.Background(), "state", map[string]Question{
		"needs_action": Noul("q", "y", "n"),
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error message carries the API key: %v", err)
	}
	if !strings.Contains(err.Error(), Redacted) {
		t.Errorf("error message does not show the redaction marker: %v", err)
	}
	if redacted := client(t, srv.URL, time.Second).Redact("prefix " + testKey); strings.Contains(redacted, testKey) {
		t.Errorf("Redact left the key in %q", redacted)
	}
}

func TestDecideRejectsMalformedQuestionsWithoutSending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a malformed call must not be sent")
	}))
	defer srv.Close()

	c := client(t, srv.URL, time.Second)
	for name, questions := range map[string]map[string]Question{
		"no questions":   {},
		"no id":          {"": Noul("q", "y", "n")},
		"no wording":     {"needs_action": {Type: "noul"}},
		"other question": {"needs_action": {Type: "choice", Instructions: "pick"}},
	} {
		if _, err := c.Decide(context.Background(), "state", questions); KindOf(err) != KindRequest {
			t.Errorf("%s: kind = %q, want %q", name, KindOf(err), KindRequest)
		}
	}
}

func TestNewClientNeedsAKeyAndAnAbsoluteBase(t *testing.T) {
	if _, err := NewClient(Config{APIKey: ""}); err == nil {
		t.Fatal("want an error without a key")
	}
	if _, err := NewClient(Config{APIKey: testKey, APIBase: "api.typesafe.ai"}); err == nil {
		t.Fatal("want an error for a base that is not an absolute URL")
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}

	if _, ok := ConfigFromEnv(env(nil)); ok {
		t.Fatal("no key must not resolve a config")
	}
	if _, ok := ConfigFromEnv(env(map[string]string{DefaultAPIKeyEnv: "  "})); ok {
		t.Fatal("a blank key must not resolve a config")
	}

	cfg, ok := ConfigFromEnv(env(map[string]string{DefaultAPIKeyEnv: testKey}))
	if !ok {
		t.Fatal("the vendor's key variable must resolve a config")
	}
	if cfg.APIBase != DefaultAPIBase || cfg.Model != DefaultModel || cfg.Timeout != DefaultTimeout {
		t.Errorf("defaults not applied: %+v", cfg)
	}

	cfg, ok = ConfigFromEnv(env(map[string]string{
		"JEV_API_KEY":    "specific",
		DefaultAPIKeyEnv: testKey,
		"JEV_API_BASE":   "https://openrouter.example/v1",
		"JEV_MODEL":      "typesafe/jev-1.13",
		"JEV_TIMEOUT_MS": "2500",
	}))
	if !ok {
		t.Fatal("want a config")
	}
	if cfg.APIKey != "specific" {
		t.Errorf("key = %q, want JEV_API_KEY to win", cfg.APIKey)
	}
	if cfg.APIBase != "https://openrouter.example/v1" || cfg.Model != "typesafe/jev-1.13" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.Timeout != 2500*time.Millisecond {
		t.Errorf("timeout = %v, want 2.5s", cfg.Timeout)
	}

	if cfg, _ := ConfigFromEnv(env(map[string]string{DefaultAPIKeyEnv: testKey, "JEV_TIMEOUT_MS": "soon"})); cfg.Timeout != DefaultTimeout {
		t.Errorf("a non-numeric timeout must fall back to the default, got %v", cfg.Timeout)
	}
}
