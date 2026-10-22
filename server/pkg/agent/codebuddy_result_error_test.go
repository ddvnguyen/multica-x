package agent

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestCodebuddyExecuteSurfacesResultErrors pins that a failed CodeBuddy run
// names its cause.
//
// The shipped protocol puts the reason of a failed run in `errors` /
// `errors_info` and leaves `result` empty — the shape a Freebuff turn comes
// back in when the account host refuses the request. A reader who only sees
// "returned an error result without details" learns nothing; the upstream's
// own message ("402 Out of credits", "No runId found in request body") is
// what makes the failure actionable.
func TestCodebuddyExecuteSurfacesResultErrors(t *testing.T) {
	fakePath := t.TempDir() + "/codebuddy"
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`printf '%s\n' '{"type":"system","session_id":"sess-cb-err"}'` + "\n" +
		`printf '%s\n' '{"type":"assistant","message":{"role":"assistant","model":"z-ai/glm-5.3-flash","content":[{"type":"text","text":"402 {\"message\":\"Out of credits.\"}"}]}}'` + "\n" +
		`printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-cb-err","errors":["402 {\"message\":\"Out of credits. Please add credits at https://www.codebuff.com/usage.\"}"],"errors_info":[{"status":402,"code":402,"details":"402 {\"message\":\"Out of credits. Please add credits at https://www.codebuff.com/usage.\"}"}]}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	b := &codebuddyBackend{cfg: Config{ExecutablePath: fakePath, Logger: slog.Default()}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := b.Execute(ctx, "say hello", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "failed" {
			t.Fatalf("status = %q, want failed (error=%q)", result.Status, result.Error)
		}
		if !strings.Contains(result.Error, "Out of credits") {
			t.Fatalf("error does not name the cause: %q", result.Error)
		}
		if strings.Contains(result.Error, "without details") {
			t.Fatalf("error is still the generic one: %q", result.Error)
		}
		if result.Output != "" {
			t.Fatalf("a failed run must publish no output, got %q", result.Output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

// TestCodebuddyResultErrorText pins how the reason of a failed result is
// read: the plain `errors` strings, the structured `errors_info` details
// (prefixed with their status when the text does not carry it), and nothing
// at all when the result names no cause.
func TestCodebuddyResultErrorText(t *testing.T) {
	cases := []struct {
		name string
		msg  codebuddySDKMessage
		want string
	}{
		{
			name: "errors only",
			msg:  codebuddySDKMessage{Errors: []string{"boom"}},
			want: "boom",
		},
		{
			name: "errors_info with a status",
			msg: codebuddySDKMessage{
				ErrorsInfo: []codebuddyErrorInfo{{Status: 402, Details: "out of credits"}},
			},
			want: "HTTP 402: out of credits",
		},
		{
			name: "errors_info whose detail already names the status",
			msg: codebuddySDKMessage{
				ErrorsInfo: []codebuddyErrorInfo{{Status: 400, Details: "400 No runId found in request body"}},
			},
			want: "400 No runId found in request body",
		},
		{
			name: "both",
			msg: codebuddySDKMessage{
				Errors:     []string{"first"},
				ErrorsInfo: []codebuddyErrorInfo{{Status: 500, Details: "second"}},
			},
			want: "first; HTTP 500: second",
		},
		{
			name: "nothing to report",
			msg:  codebuddySDKMessage{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codebuddyResultErrorText(tc.msg); got != tc.want {
				t.Fatalf("codebuddyResultErrorText() = %q, want %q", got, tc.want)
			}
		})
	}
}
