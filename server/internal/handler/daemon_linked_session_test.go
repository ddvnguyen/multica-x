package handler

// S3-B2 (M-138): the server half of the seeded start. Two things are worth
// pinning here without a database: which linked issue's session wins, and that
// the verdict reaches the run row's result JSONB (it is the only place a
// reader can tell a seeded run from a warm one).

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func linkedCandidate(sessionID string, endedAt time.Time, runtimeID pgtype.UUID) linkedPriorSessionCandidate {
	return linkedPriorSessionCandidate{
		issueID:    pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		SessionID:  pgtype.Text{String: sessionID, Valid: sessionID != ""},
		WorkDir:    pgtype.Text{String: "/work/" + sessionID, Valid: sessionID != ""},
		TerminalAt: pgtype.Timestamptz{Time: endedAt, Valid: !endedAt.IsZero()},
		runtimeID:  runtimeID,
	}
}

func TestNewestLinkedPriorSessionRanksAndFilters(t *testing.T) {
	t.Parallel()

	dshRuntime := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	otherRuntime := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		candidates []linkedPriorSessionCandidate
		want       string
	}{
		{
			name: "newest linked session wins",
			candidates: []linkedPriorSessionCandidate{
				linkedCandidate("older", base.Add(-time.Hour), dshRuntime),
				linkedCandidate("newer", base, dshRuntime),
			},
			want: "newer",
		},
		{
			name: "a session on another runtime is not a fork source",
			candidates: []linkedPriorSessionCandidate{
				linkedCandidate("other-runtime", base, otherRuntime),
				linkedCandidate("dsh", base.Add(-time.Hour), dshRuntime),
			},
			want: "dsh",
		},
		{
			name: "a session with no end time cannot date warm or cold",
			candidates: []linkedPriorSessionCandidate{
				linkedCandidate("undated", time.Time{}, dshRuntime),
				linkedCandidate("dated", base.Add(-time.Hour), dshRuntime),
			},
			want: "dated",
		},
		{
			name: "a row with no session id is skipped",
			candidates: []linkedPriorSessionCandidate{
				linkedCandidate("", base, dshRuntime),
			},
			want: "",
		},
		{
			name:       "no linked issue had a session",
			candidates: nil,
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := newestLinkedPriorSession(tc.candidates, dshRuntime)
			if tc.want == "" {
				if ok {
					t.Fatalf("got %q, want no candidate", got.SessionID.String)
				}
				return
			}
			if !ok || got.SessionID.String != tc.want {
				t.Fatalf("got %q (ok=%v), want %q", got.SessionID.String, ok, tc.want)
			}
			if got.WorkDir.String != "/work/"+tc.want {
				t.Fatalf("work dir = %q, want the winning session's", got.WorkDir.String)
			}
		})
	}
}

// The verdict is only useful if it survives the trip into agent_task_queue.result:
// the completion handler marshals the request as-is, so the field has to be on
// the request struct with a JSON tag and must be scrubbed like every other
// caller-supplied string.
func TestTaskCompleteRequestCarriesSessionStart(t *testing.T) {
	t.Parallel()

	req := TaskCompleteRequest{Output: "done", SessionStart: "seeded from summary of sess-1\x00"}
	sanitizeTaskCompleteRequest(&req)
	if req.SessionStart != "seeded from summary of sess-1" {
		t.Fatalf("session_start = %q, want the NUL stripped", req.SessionStart)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, _ := decoded["session_start"].(string); got != "seeded from summary of sess-1" {
		t.Fatalf("result JSON session_start = %v, want the verdict", decoded["session_start"])
	}

	// An empty verdict stays off the row: "not measured" and "fresh" are
	// different facts and must not read the same.
	empty, err := json.Marshal(TaskCompleteRequest{Output: "done"})
	if err != nil {
		t.Fatal(err)
	}
	var omitted map[string]any
	if err := json.Unmarshal(empty, &omitted); err != nil {
		t.Fatal(err)
	}
	if _, present := omitted["session_start"]; present {
		t.Fatalf("session_start present on an unmeasured run: %s", empty)
	}
}
