package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

type issueDecision struct {
	ID        string `json:"id"`
	Problem   string `json:"problem"`
	Decided   string `json:"decided"`
	Rationale string `json:"rationale"`
	Scope     string `json:"scope"`
}

var issueDecisionsCmd = &cobra.Command{
	Use:   "decisions <id>",
	Short: "List current Owner decisions for an issue",
	Args:  exactArgs(1),
	RunE:  runIssueDecisions,
}

func init() {
	issueDecisionsCmd.Flags().String("output", "table", "Output format: table or json")
	issueDecisionsCmd.Flags().Bool("all", false, "Include superseded decisions")
	issueCmd.AddCommand(issueDecisionsCmd)
	issueDecisionCmd.AddCommand(issueDecisionAddCmd)
	issueCmd.AddCommand(issueDecisionCmd)
}

var issueDecisionCmd = &cobra.Command{Use: "decision", Short: "Record an Owner decision on an issue"}
var issueDecisionAddCmd = &cobra.Command{Use: "add <issue-id>", Short: "Record a human-resolved decision", Args: exactArgs(1), RunE: runIssueDecisionAdd}

func init() {
	issueDecisionAddCmd.Flags().String("problem", "", "Question or problem being resolved")
	issueDecisionAddCmd.Flags().String("decided", "", "Human resolution")
	issueDecisionAddCmd.Flags().String("comment", "", "ID of the comment recording the resolution: a human comment, or the system mirror of an answered ask card")
	issueDecisionAddCmd.Flags().String("problem-comment", "", "ID of the comment containing the question or problem")
	issueDecisionAddCmd.Flags().String("scope", "issue", "Decision scope: issue or project")
	issueDecisionAddCmd.Flags().String("output", "table", "Output format: table or json")
	_ = issueDecisionAddCmd.MarkFlagRequired("problem")
	_ = issueDecisionAddCmd.MarkFlagRequired("decided")
	_ = issueDecisionAddCmd.MarkFlagRequired("comment")
}

func runIssueDecisions(cmd *cobra.Command, args []string) error {
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
	var records []issueDecision
	all, _ := cmd.Flags().GetBool("all")
	path := "/api/issues/" + url.PathEscape(issueRef.ID) + "/decision-records?current=true"
	if all {
		path = "/api/issues/" + url.PathEscape(issueRef.ID) + "/decision-records"
	}
	if err := client.GetJSON(ctx, path, &records); err != nil {
		return fmt.Errorf("list current issue decisions: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, records)
	}
	if output != "table" {
		return fmt.Errorf("invalid output %q; valid values: table, json", output)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if len(records) == 0 {
		fmt.Fprintln(w, "No current decisions.")
	} else {
		fmt.Fprintln(w, "ID\tPROBLEM\tDECIDED\tSCOPE\tRATIONALE")
		for _, record := range records {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", record.ID, record.Problem, record.Decided, record.Scope, record.Rationale)
		}
	}
	return w.Flush()
}

func runIssueDecisionAdd(cmd *cobra.Command, args []string) error {
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
	problem, _ := cmd.Flags().GetString("problem")
	decided, _ := cmd.Flags().GetString("decided")
	commentID, _ := cmd.Flags().GetString("comment")
	problemCommentID, _ := cmd.Flags().GetString("problem-comment")
	supersedes, _ := cmd.Flags().GetString("supersedes")
	scope, _ := cmd.Flags().GetString("scope")
	if scope != "issue" && scope != "project" {
		return fmt.Errorf("invalid scope %q; valid values: issue, project", scope)
	}
	body := map[string]any{"problem": problem, "decided": decided, "comment_id": commentID, "scope": scope}
	if problemCommentID != "" {
		body["problem_comment_id"] = problemCommentID
	}
	if supersedes != "" {
		body["supersedes_id"] = supersedes
	}
	var record issueDecision
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(issueRef.ID)+"/decision-records", body, &record); err != nil {
		return fmt.Errorf("record issue decision: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, record)
	}
	if output != "table" {
		return fmt.Errorf("invalid output %q; valid values: table, json", output)
	}
	fmt.Fprintf(os.Stdout, "Decision recorded: %s\n%s → %s\n", record.ID, record.Problem, record.Decided)
	return nil
}
