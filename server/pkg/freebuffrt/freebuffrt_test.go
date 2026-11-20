package freebuffrt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReady(t *testing.T) {
	r, ok := ParseReady(`[orchestrator-ready] {"launchId":"abc","pid":12,"port":4321}`)
	if !ok || r.Port != 4321 || r.PID != 12 || r.LaunchID != "abc" {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
	for _, bad := range []string{"", "hello", "[orchestrator-ready] nope", `[orchestrator-ready] {"port":0}`, `[orchestrator-ready] {"port":70000}`} {
		if _, ok := ParseReady(bad); ok {
			t.Errorf("ParseReady(%q) accepted", bad)
		}
	}
}

func TestResolveBundle(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "orchestrator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orchestrator.js"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]map[string]string{
		"runtime dir": {EnvRuntimeDir: root},
		"app path":    {EnvAppPath: root},
		"entry file":  {EnvAppPath: filepath.Join(dir, "orchestrator.js")},
	} {
		b, err := ResolveBundle(env, t.TempDir())
		if err != nil || b.ResourcesDir != root {
			t.Errorf("%s: got %+v err=%v", name, b, err)
		}
	}
	if _, err := ResolveBundle(map[string]string{}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "bundle not found") {
		t.Errorf("want not-found error, got %v", err)
	}
}

func TestLaunchEnv(t *testing.T) {
	env := LaunchEnv(Bundle{ResourcesDir: "/r"}, "/p", "id1")
	want := map[string]string{
		"PORT":                          "0",
		"FREEBUFF_LAUNCH_ID":            "id1",
		"FREEBUFF_DESKTOP_STATE_PATH":   "/p/state.json",
		"FREEBUFF_UI_DIR":               "/r/orchestrator/ui",
		"FREEBUFF_PROFILE_LOCK_WAIT_MS": "0",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if err := ValidateProfileDir("rel"); err == nil {
		t.Error("relative profile dir accepted")
	}
}

// The orchestrator reads FREEBUFF_DESKTOP_STATE_PATH as a JSON file and
// rewrites it on sign-in. Pointing it at a directory makes every read fail, so
// the runtime reports the account as signed out and a fresh sign-in cannot be
// stored — the account then looks logged out again on the next launch.
func TestLaunchEnvStatePathIsAFile(t *testing.T) {
	dir := t.TempDir()
	got := LaunchEnv(Bundle{ResourcesDir: "/r"}, dir, "id1")["FREEBUFF_DESKTOP_STATE_PATH"]
	if got != StateFilePath(dir) {
		t.Fatalf("state path = %q, want %q", got, StateFilePath(dir))
	}
	if filepath.Base(got) != "state.json" {
		t.Fatalf("state path %q is not a state.json file", got)
	}
	if fi, err := os.Stat(got); err == nil && fi.IsDir() {
		t.Fatalf("state path %q is a directory", got)
	}
}

func fakeOrchestrator(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(launchHeader) != "tok" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClientForURL(srv.URL, "tok")
}

func TestClientCalls(t *testing.T) {
	var gotMessage map[string]any
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /healthz":
			_, _ = w.Write([]byte(`{"ok":true,"launchId":"tok"}`))
		case "GET /api/auth/status":
			_, _ = w.Write([]byte(`{"authed":true,"user":{"email":"a@b"},"login":{"phase":"idle"}}`))
		case "POST /api/threads":
			_, _ = w.Write([]byte(`{"id":"t1","turnState":"idle"}`))
		case "POST /api/thread/t1/message":
			_ = json.NewDecoder(r.Body).Decode(&gotMessage)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "GET /api/thread/missing":
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	ctx := context.Background()
	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := c.AuthStatus(ctx)
	if err != nil || !st.Authed || !strings.Contains(string(st.User), "a@b") {
		t.Fatalf("auth status: %+v %v", st, err)
	}
	th, err := c.CreateThread(ctx, "/work")
	if err != nil || th.ID != "t1" {
		t.Fatalf("create thread: %+v %v", th, err)
	}
	if err := c.PostMessage(ctx, "t1", "hi"); err != nil || gotMessage["text"] != "hi" {
		t.Fatalf("post message: %v %v", gotMessage, err)
	}
	_, err = c.GetThread(ctx, "missing")
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != 404 || apiErr.Message != "not found" {
		t.Fatalf("want 404 APIError, got %#v", err)
	}
}

func TestClientRejectsWrongLaunchID(t *testing.T) {
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {})
	c.launchID = "other"
	if err := c.Health(context.Background()); err == nil {
		t.Fatal("want unauthorized")
	}
}

func TestEventsStream(t *testing.T) {
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(": ping\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"text\",\"text\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: not json\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"finish\"}\n\n"))
	})
	ch, wait, err := c.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for ev := range ch {
		types = append(types, ev.Type)
	}
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(types, ",") != "text,finish" {
		t.Fatalf("types = %v", types)
	}
}

func TestStartLoginResolvesTheRemoteSignInURL(t *testing.T) {
	var base string
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/auth/login/start":
			_, _ = w.Write([]byte(`{"url":"` + base + `/auth/login/launch/tok","attemptId":"at1"}`))
		case "GET /auth/login/launch/tok":
			http.Redirect(w, r, "https://freebuff.com/login?auth_code=abc&desktop_flow=at1", http.StatusFound)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	base = c.base
	got, err := c.StartLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.RemoteURL != "https://freebuff.com/login?auth_code=abc&desktop_flow=at1" {
		t.Fatalf("remote URL = %q", got.RemoteURL)
	}
	if got.AttemptID != "at1" || !strings.HasPrefix(got.URL, base) {
		t.Fatalf("loopback fields lost: %+v", got)
	}
}

func TestStartLoginFallsBackWhenTheRedirectIsUnusable(t *testing.T) {
	var base string
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login/start":
			_, _ = w.Write([]byte(`{"url":"` + base + `/auth/login/launch/tok","attemptId":"at1"}`))
		default:
			http.Redirect(w, r, "http://127.0.0.1:1/loop", http.StatusFound)
		}
	})
	base = c.base
	got, err := c.StartLogin(context.Background())
	if err != nil || got.RemoteURL != "" {
		t.Fatalf("a loopback redirect must not be offered as a remote URL: %+v %v", got, err)
	}
}

func TestModelCatalogReadsTheFirstStateEvent(t *testing.T) {
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "GET /api/events" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(": hello\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"other"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"state","snapshot":{"freebuff":{"models":[{"id":"luna","displayName":"Luna 2"},{"id":""},{"id":"solar","displayName":"Solar Pro","tagline":"free tier"}],"recommendedModels":["luna"]}}}` + "\n\n"))
	})
	cat, err := c.ModelCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cat.Default != "luna" {
		t.Fatalf("default = %q, want luna", cat.Default)
	}
	if len(cat.Models) != 2 {
		t.Fatalf("models = %+v", cat.Models)
	}
	if cat.Models[0].ID != "luna" || cat.Models[0].DisplayName != "Luna 2" {
		t.Fatalf("first model = %+v", cat.Models[0])
	}
	if cat.Models[1].ID != "solar" || cat.Models[1].Tagline != "free tier" {
		t.Fatalf("second model = %+v", cat.Models[1])
	}
}

func TestModelCatalogErrorsWithoutAPublishedCatalog(t *testing.T) {
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"state","snapshot":{"freebuff":{}}}` + "\n\n"))
	})
	if _, err := c.ModelCatalog(context.Background()); err == nil {
		t.Fatal("want an error when no state event publishes models")
	}
}

func TestModelCatalogSurfacesAPIError(t *testing.T) {
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no stream for you"}`, http.StatusForbidden)
	})
	_, err := c.ModelCatalog(context.Background())
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != http.StatusForbidden {
		t.Fatalf("want APIError 403, got %#v", err)
	}
}

func TestCreateThreadWithModelBody(t *testing.T) {
	var bodies []map[string]any
	c := fakeOrchestrator(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "POST /api/threads" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"id":"t1"}`))
	})
	th, err := c.CreateThreadWithModel(context.Background(), "/work", "")
	if err != nil || th.ID != "t1" {
		t.Fatalf("create with empty model: %+v %v", th, err)
	}
	th, err = c.CreateThreadWithModel(context.Background(), "/work", "luna")
	if err != nil || th.ID != "t1" {
		t.Fatalf("create with model: %+v %v", th, err)
	}
	want := []map[string]any{
		{"projectPath": "/work"},
		{"projectPath": "/work", "harnessId": "codebuff", "model": "luna"},
	}
	if len(bodies) != len(want) {
		t.Fatalf("bodies = %+v", bodies)
	}
	for i, w := range want {
		for k, v := range w {
			if bodies[i][k] != v {
				t.Fatalf("body %d field %s = %v, want %v", i, k, bodies[i][k], v)
			}
		}
	}
}
