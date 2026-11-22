package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// dshSessionURL is the "Open session" href (M-132 E1): the operator's dsh web
// base plus the run's own session id, so two runs never share one link.
func TestDshSessionURL(t *testing.T) {
	cases := []struct {
		name      string
		base      string
		sessionID string
		want      string
	}{
		{
			name:      "adds the session parameter",
			base:      "http://127.0.0.1:4320",
			sessionID: "ses-abc",
			want:      "http://127.0.0.1:4320?dsh_session=ses-abc",
		},
		{
			name:      "a trailing slash does not double up",
			base:      "https://dsh.example.com/",
			sessionID: "ses.abc_1",
			want:      "https://dsh.example.com/?dsh_session=ses.abc_1",
		},
		{
			name:      "keeps a deployed path prefix",
			base:      "https://mmx.example.com/dsh/",
			sessionID: "ses-abc",
			want:      "https://mmx.example.com/dsh/?dsh_session=ses-abc",
		},
		{
			name:      "keeps an operator's existing query",
			base:      "https://dsh.example.com/?theme=dark",
			sessionID: "ses-abc",
			want:      "https://dsh.example.com/?dsh_session=ses-abc&theme=dark",
		},
		{
			name:      "replaces a stale session parameter",
			base:      "https://dsh.example.com/?dsh_session=old",
			sessionID: "ses-new",
			want:      "https://dsh.example.com/?dsh_session=ses-new",
		},
		{
			name:      "trims a padded session id",
			base:      "https://dsh.example.com",
			sessionID: "  ses-abc  ",
			want:      "https://dsh.example.com?dsh_session=ses-abc",
		},
		{
			name:      "no session means no link",
			base:      "https://dsh.example.com",
			sessionID: "",
			want:      "",
		},
		{
			name:      "a blank session means no link",
			base:      "https://dsh.example.com",
			sessionID: "   ",
			want:      "",
		},
		{
			name:      "a relative base keeps the operator's link and adds nothing",
			base:      "dsh.example.com/",
			sessionID: "ses-abc",
			want:      "dsh.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dshSessionURL(tc.base, tc.sessionID); got != tc.want {
				t.Fatalf("dshSessionURL(%q, %q) = %q, want %q", tc.base, tc.sessionID, got, tc.want)
			}
		})
	}
}

// The hydration rule: only dsh-owned runs with a recorded session get the link,
// and each one addresses its own session.
func TestStampDshSessionURLs(t *testing.T) {
	dshRuntime := uuid.New()
	otherRuntime := uuid.New()

	rows := []db.AgentTaskQueue{
		{RuntimeID: dshTestUUID(dshRuntime), SessionID: dshTestText("ses-first")},
		{RuntimeID: dshTestUUID(dshRuntime), SessionID: dshTestText("ses-second")},
		{RuntimeID: dshTestUUID(dshRuntime)},
		{RuntimeID: dshTestUUID(otherRuntime), SessionID: dshTestText("ses-other")},
	}
	resp := make([]AgentTaskResponse, len(rows))

	stampDshSessionURLs(rows, resp, map[string]bool{dshRuntime.String(): true}, "https://dsh.example.com/")

	want := []string{
		"https://dsh.example.com/?dsh_session=ses-first",
		"https://dsh.example.com/?dsh_session=ses-second",
		"",
		"",
	}
	for i := range resp {
		if resp[i].DshSessionURL != want[i] {
			t.Fatalf("row %d: dsh_session_url = %q, want %q", i, resp[i].DshSessionURL, want[i])
		}
	}
	if resp[0].DshSessionURL == resp[1].DshSessionURL {
		t.Fatalf("two sessions share one link: %q", resp[0].DshSessionURL)
	}
}

// A response slice shorter than the row slice must not panic or overrun; the
// pair is always built together, and this guard is what keeps a future caller
// that filters one side from indexing past the end.
func TestStampDshSessionURLsIgnoresRowsWithoutResponse(t *testing.T) {
	runtime := uuid.New()
	rows := []db.AgentTaskQueue{{RuntimeID: dshTestUUID(runtime), SessionID: dshTestText("ses-abc")}}
	resp := []AgentTaskResponse{}
	stampDshSessionURLs(rows, resp, map[string]bool{runtime.String(): true}, "https://dsh.example.com")
	if len(resp) != 0 {
		t.Fatalf("response grew to %d rows", len(resp))
	}
}

func dshTestUUID(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: value, Valid: true}
}

func dshTestText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}
