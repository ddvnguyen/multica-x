package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// newQuestionTestCmd mirrors the flag set of the real subcommands so a test can
// drive the run function without mutating the package-level command's flag
// state (which would leak between tests).
func newQuestionTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "question"}
	cmd.Flags().String("status", "", "")
	cmd.Flags().Int("limit", 50, "")
	cmd.Flags().String("option", "", "")
	cmd.Flags().String("text", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().Bool("full-id", false, "")
	cmd.Flags().String("profile", "", "")
	return cmd
}

const testQuestionIssueID = "11111111-1111-7111-8111-111111111111"

func TestRunQuestionListRequestsIssueQuestions(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if want := "/api/issues/" + testQuestionIssueID + "/questions"; r.URL.Path != want {
			t.Fatalf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.URL.Query().Get("status"); got != "open" {
			t.Errorf("status = %q, want open", got)
		}
		if got := r.URL.Query().Get("limit"); got != "7" {
			t.Errorf("limit = %q, want 7", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"questions": []map[string]any{{
				"id":         "22222222-2222-7222-8222-222222222222",
				"issue_id":   testQuestionIssueID,
				"header":     "Database",
				"prompt":     "Which database should the migration target?",
				"status":     "open",
				"expires_at": "2099-01-01T00:00:00Z",
			}},
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newQuestionTestCmd()
	_ = cmd.Flags().Set("status", "open")
	_ = cmd.Flags().Set("limit", "7")

	out, err := captureStdout(t, func() error { return runQuestionList(cmd, []string{testQuestionIssueID}) })
	if err != nil {
		t.Fatalf("runQuestionList: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("stdout must be JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0]["prompt"] != "Which database should the migration target?" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func TestRunQuestionAnswerPostsOption(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")

	const questionID = "33333333-3333-7333-8333-333333333333"
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if want := "/api/questions/" + questionID + "/answer"; r.URL.Path != want {
			t.Fatalf("path = %q, want %q", r.URL.Path, want)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":               questionID,
			"status":           "answered",
			"answer":           "Postgres",
			"answered_by_type": "agent",
			"expires_at":       "2099-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newQuestionTestCmd()
	_ = cmd.Flags().Set("option", "Postgres")

	out, err := captureStdout(t, func() error { return runQuestionAnswer(cmd, []string{questionID}) })
	if err != nil {
		t.Fatalf("runQuestionAnswer: %v", err)
	}
	if gotBody["answer"] != "Postgres" {
		t.Fatalf("answer body = %#v, want Postgres", gotBody["answer"])
	}
	if !strings.Contains(out, `"status": "answered"`) {
		t.Fatalf("output missing status: %s", out)
	}
}

func TestRunQuestionAnswerAcceptsFreeText(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "q-1", "status": "answered", "answer": "use sqlite"})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newQuestionTestCmd()
	_ = cmd.Flags().Set("text", "  use sqlite  ")

	if _, err := captureStdout(t, func() error { return runQuestionAnswer(cmd, []string{"q-1"}) }); err != nil {
		t.Fatalf("runQuestionAnswer: %v", err)
	}
	if gotBody["answer"] != "use sqlite" {
		t.Fatalf("answer body = %#v, want trimmed free text", gotBody["answer"])
	}
}

func TestRunQuestionAnswerRequiresExactlyOneSource(t *testing.T) {
	cases := []struct {
		name   string
		option string
		text   string
		want   string
	}{
		{name: "neither", want: "an answer is required"},
		{name: "both", option: "A", text: "B", want: "not both"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newQuestionTestCmd()
			_ = cmd.Flags().Set("option", tc.option)
			_ = cmd.Flags().Set("text", tc.text)
			err := runQuestionAnswer(cmd, []string{"q-1"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
