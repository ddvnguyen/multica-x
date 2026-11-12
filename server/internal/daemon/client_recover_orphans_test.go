package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_RecoverOrphans_DecodesRunningDetached(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantDetached bool
		wantTasks    []RunningDetachedTask
	}{
		{
			name:         "with running_detached",
			body:         `{"orphaned":0,"retried":0,"detached":true,"running_detached":[{"task_id":"t-1","work_dir":"/w/1","session_id":"s-1"},{"task_id":"t-2","work_dir":"","session_id":""}],"future_field":1}`,
			wantDetached: true,
			wantTasks: []RunningDetachedTask{
				{TaskID: "t-1", WorkDir: "/w/1", SessionID: "s-1"},
				{TaskID: "t-2"},
			},
		},
		{
			name:         "detached without the field (older server)",
			body:         `{"orphaned":0,"retried":0,"detached":true}`,
			wantDetached: true,
		},
		{
			name: "non-detached response",
			body: `{"orphaned":2,"retried":1}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/daemon/runtimes/rt-1/recover-orphans" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient(srv.URL)
			got, err := c.RecoverOrphans(context.Background(), "rt-1")
			if err != nil {
				t.Fatalf("RecoverOrphans: %v", err)
			}
			if got.Detached != tc.wantDetached {
				t.Errorf("Detached = %v, want %v", got.Detached, tc.wantDetached)
			}
			if len(got.RunningDetached) != len(tc.wantTasks) {
				t.Fatalf("RunningDetached = %+v, want %+v", got.RunningDetached, tc.wantTasks)
			}
			for i, want := range tc.wantTasks {
				if got.RunningDetached[i] != want {
					t.Errorf("RunningDetached[%d] = %+v, want %+v", i, got.RunningDetached[i], want)
				}
			}
		})
	}
}
