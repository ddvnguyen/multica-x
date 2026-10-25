package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// multica question {list|answer} — the agent-facing half of structured
// questions. A run whose tool call is parked on an owner decision uses these
// to find the question and to answer it on the owner's behalf; the same
// records are rendered as a card on the issue for a human to answer.
//
// Answering with an agent token is recorded as that agent (answered_by), so
// the issue shows who decided — the platform does not silently launder an
// agent's choice as the owner's.

var questionCmd = &cobra.Command{
	Use:   "question",
	Short: "List and answer structured agent questions",
	Long: `List and answer structured agent questions.

A run that needs a decision parks its tool call and records a question on the
issue. Answer it from here with an option label or free text; the waiting run
resumes with the answer.`,
}

var questionListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "List questions asked on an issue",
	Args:  exactArgs(1),
	RunE:  runQuestionList,
}

var questionAnswerCmd = &cobra.Command{
	Use:   "answer <question-id>",
	Short: "Answer an open question",
	Long: `Answer an open question.

Pass --option with one of the question's option labels, or --text with free
text (the "Other" answer). Exactly one is required. A multi-select question
takes comma-separated labels.`,
	Args: exactArgs(1),
	RunE: runQuestionAnswer,
}

func init() {
	questionCmd.AddCommand(questionListCmd)
	questionCmd.AddCommand(questionAnswerCmd)

	questionListCmd.Flags().String("status", "", "Filter by status: open, answered or expired (default: all)")
	questionListCmd.Flags().Int("limit", 50, "Maximum number of questions to return (1-200)")
	questionListCmd.Flags().String("output", "table", "Output format: table or json")
	questionListCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")

	questionAnswerCmd.Flags().String("option", "", "Option label to answer with")
	questionAnswerCmd.Flags().String("text", "", "Free-text answer (the \"Other\" field)")
	questionAnswerCmd.Flags().String("output", "table", "Output format: table or json")
	questionAnswerCmd.Flags().Bool("full-id", false, "Show full UUIDs in table output")
}

func runQuestionList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}

	path := "/api/issues/" + issueRef.ID + "/questions"
	query := make([]string, 0, 2)
	if status, _ := cmd.Flags().GetString("status"); status != "" {
		query = append(query, "status="+status)
	}
	if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
		query = append(query, fmt.Sprintf("limit=%d", limit))
	}
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("list questions: %w", err)
	}
	questions, _ := result["questions"].([]any)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, questions)
	}
	fullID, _ := cmd.Flags().GetBool("full-id")
	printQuestionTable(questions, fullID)
	return nil
}

func runQuestionAnswer(cmd *cobra.Command, args []string) error {
	option, _ := cmd.Flags().GetString("option")
	text, _ := cmd.Flags().GetString("text")
	option = strings.TrimSpace(option)
	text = strings.TrimSpace(text)
	switch {
	case option != "" && text != "":
		return fmt.Errorf("pass either --option or --text, not both")
	case option == "" && text == "":
		return fmt.Errorf("an answer is required: pass --option <label> or --text <answer>")
	}
	answer := option
	if answer == "" {
		answer = text
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var question map[string]any
	if err := client.PostJSON(ctx, "/api/questions/"+strings.TrimSpace(args[0])+"/answer", map[string]any{"answer": answer}, &question); err != nil {
		return fmt.Errorf("answer question: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, question)
	}
	fullID, _ := cmd.Flags().GetBool("full-id")
	printQuestionTable([]any{question}, fullID)
	return nil
}

func printQuestionTable(questions []any, fullID bool) {
	headers := []string{"ID", "STATUS", "HEADER", "PROMPT", "ANSWER", "EXPIRES"}
	rows := make([][]string, 0, len(questions))
	for _, raw := range questions {
		q, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			displayID(strVal(q, "id"), fullID),
			strVal(q, "status"),
			strVal(q, "header"),
			truncateQuestionText(strVal(q, "prompt")),
			truncateQuestionText(strVal(q, "answer")),
			formatQuestionDeadline(q),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
}

// truncateQuestionText keeps a table row readable: prompts are full sentences
// and would otherwise push the answer off screen. JSON output is untruncated.
// Counted in runes, not bytes: a byte slice would cut a CJK prompt mid-rune.
func truncateQuestionText(s string) string {
	const max = 60
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// formatQuestionDeadline renders the deadline in local time and marks one that
// has already passed, so an expired-but-unflushed question reads as expired
// rather than as something still worth answering.
func formatQuestionDeadline(q map[string]any) string {
	raw := strVal(q, "expires_at")
	if raw == "" {
		return ""
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	if !at.After(time.Now()) {
		return "expired"
	}
	return at.Local().Format("2006-01-02 15:04")
}
