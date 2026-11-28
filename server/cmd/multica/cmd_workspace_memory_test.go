package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const memoryComparisonFixture = `{
  "workspace_id":"workspace-1","marked":4,
  "votes":{"moct":2,"powercontext":1,"neither":1},
  "preference_pct":{"moct":50,"powercontext":25,"neither":25},
  "shadow_status":{"with_block":3,"without_block":1},
  "backends":[
    {"backend":"moct","bytes":900,"latency_ms":40},
    {"backend":"powercontext","bytes":1200,"latency_ms":150}
  ]
}`

func TestWorkspaceMemoryCompareSendsSinceAndPrintsAggregate(t *testing.T) {
	const since = "2026-11-28T00:00:00Z"
	var gotPath, gotQuery string
	useMemoryTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(memoryComparisonFixture))
	})
	setMemoryCmdFlag(t, workspaceMemoryCompareCmd, "since", since)
	setMemoryCmdFlag(t, workspaceMemoryCompareCmd, "output", "json")

	out, err := captureStdout(t, func() error {
		return runWorkspaceMemoryCompare(workspaceMemoryCompareCmd, nil)
	})
	if err != nil {
		t.Fatalf("runWorkspaceMemoryCompare: %v", err)
	}
	if gotPath != "/api/workspaces/workspace-1/memory-comparison" {
		t.Fatalf("GET path = %q", gotPath)
	}
	if gotQuery != "since="+strings.ReplaceAll(since, ":", "%3A") {
		t.Fatalf("query = %q, want since=%s URL-encoded", gotQuery, since)
	}
	var comparison memoryComparisonResponse
	if err := json.Unmarshal([]byte(out), &comparison); err != nil {
		t.Fatalf("decode CLI JSON %q: %v", out, err)
	}
	if comparison.Marked != 4 || comparison.Votes.Moct != 2 || comparison.Votes.PowerContext != 1 ||
		comparison.PreferencePct.Moct != 50 || comparison.ShadowStatus.WithBlock != 3 ||
		comparison.ShadowStatus.WithoutBlock != 1 {
		t.Fatalf("aggregate = %+v", comparison)
	}
	if len(comparison.Backends) != 2 || comparison.Backends[1].LatencyMS != 150 {
		t.Fatalf("backends = %+v", comparison.Backends)
	}
}

func TestWorkspaceMemoryCompareTableShowsRatedRunsPreferenceAndShadow(t *testing.T) {
	var gotQuery string
	useMemoryTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(memoryComparisonFixture))
	})
	setMemoryCmdFlag(t, workspaceMemoryCompareCmd, "since", "")
	setMemoryCmdFlag(t, workspaceMemoryCompareCmd, "output", "table")

	out, err := captureStdout(t, func() error {
		return runWorkspaceMemoryCompare(workspaceMemoryCompareCmd, nil)
	})
	if err != nil {
		t.Fatalf("runWorkspaceMemoryCompare: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q, want no since filter", gotQuery)
	}
	for _, want := range []string{"RATED RUNS", "PREFERENCE", "moct=50%", "powercontext=25%",
		"VOTES", "SHADOW", "with_block=3", "without_block=1", "BACKEND"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output %q is missing %q", out, want)
		}
	}
}

func TestWorkspaceMemoryCompareRejectsAnInvalidSinceWithoutRequesting(t *testing.T) {
	requested := false
	useMemoryTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requested = true
	})
	setMemoryCmdFlag(t, workspaceMemoryCompareCmd, "since", "yesterday")

	_, err := captureStdout(t, func() error {
		return runWorkspaceMemoryCompare(workspaceMemoryCompareCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "--since must be an RFC3339 timestamp") {
		t.Fatalf("error = %v, want an RFC3339 validation error", err)
	}
	if requested {
		t.Fatal("invalid --since still reached the server")
	}
}
