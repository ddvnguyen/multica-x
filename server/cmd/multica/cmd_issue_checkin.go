package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	issueCmd.AddCommand(newIssueCheckinCommand())
}

func newIssueCheckinCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "checkin <issue-id>",
		Short: "End a run silently without posting a comment",
		Long:  "Stamps a task-level check-in on the current run so the completion fallback comment is skipped. Use when a run has no real update to post.",
		Args:  cobra.ExactArgs(1),
		RunE:  runIssueCheckin,
	}
	c.Flags().String("note", "", "What the run checked or found (required, up to 500 characters)")
	return c
}

func runIssueCheckin(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	note, _ := cmd.Flags().GetString("note")
	if note == "" {
		return fmt.Errorf("--note is required")
	}
	path := "/api/issues/" + url.PathEscape(ref.ID) + "/checkin"
	if err = client.PostJSON(ctx, path, map[string]any{"note": note}, nil); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Checked in; this run will not post a comment.")
	return nil
}
