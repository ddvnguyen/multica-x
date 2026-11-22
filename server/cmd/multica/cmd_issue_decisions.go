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
	Decided   string `json:"decided"`
	Rationale string `json:"rationale"`
}

var issueDecisionsCmd = &cobra.Command{
	Use:   "decisions <id>",
	Short: "List current Owner decisions for an issue",
	Args:  exactArgs(1),
	RunE:  runIssueDecisions,
}

func init() {
	issueDecisionsCmd.Flags().String("output", "table", "Output format: table or json")
	issueCmd.AddCommand(issueDecisionsCmd)
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
	path := "/api/issues/" + url.PathEscape(issueRef.ID) + "/decision-records?current=true"
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
		fmt.Fprintln(w, "ID\tDECIDED\tRATIONALE")
		for _, record := range records {
			fmt.Fprintf(w, "%s\t%s\t%s\n", record.ID, record.Decided, record.Rationale)
		}
	}
	return w.Flush()
}
