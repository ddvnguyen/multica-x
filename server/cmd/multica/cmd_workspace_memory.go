package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// The workspace aggregate is printed as returned: it contains counts and
// medians only, never a memory block. Explicit structs keep a future server
// field from silently leaking body text into --output json.
type memoryComparisonVotes struct {
	Moct         int64 `json:"moct"`
	PowerContext int64 `json:"powercontext"`
	Neither      int64 `json:"neither"`
}

type memoryComparisonPct struct {
	Moct         int `json:"moct"`
	PowerContext int `json:"powercontext"`
	Neither      int `json:"neither"`
}

type memoryComparisonBackend struct {
	Backend   string `json:"backend"`
	Bytes     int    `json:"bytes"`
	LatencyMS int    `json:"latency_ms"`
}

type memoryComparisonShadowStatus struct {
	WithBlock    int64 `json:"with_block"`
	WithoutBlock int64 `json:"without_block"`
}

type memoryComparisonResponse struct {
	WorkspaceID   string                       `json:"workspace_id"`
	Marked        int64                        `json:"marked"`
	Votes         memoryComparisonVotes        `json:"votes"`
	PreferencePct memoryComparisonPct          `json:"preference_pct"`
	Backends      []memoryComparisonBackend    `json:"backends"`
	ShadowStatus  memoryComparisonShadowStatus `json:"shadow_status"`
}

var workspaceMemoryCompareCmd = &cobra.Command{
	Use:   "memory-compare [workspace-id|slug|prefix]",
	Short: "Read the workspace memory comparison aggregate",
	Long: "Prints how many runs were rated, how the votes split, how many rated shadows " +
		"returned a block, and the median bytes and latency per backend. With --since, only " +
		"votes made at or after that timestamp are counted — the same window the server uses " +
		"for the panel, so two readers agree on the tally.",
	Args: cobra.MaximumNArgs(1),
	RunE: runWorkspaceMemoryCompare,
}

func init() {
	workspaceMemoryCompareCmd.Flags().String("since", "", "Only count marks at or after this RFC3339 timestamp")
	workspaceMemoryCompareCmd.Flags().String("output", "table", "Output format: table or json")
	workspaceCmd.AddCommand(workspaceMemoryCompareCmd)
}

func runWorkspaceMemoryCompare(cmd *cobra.Command, args []string) error {
	since, _ := cmd.Flags().GetString("since")
	since = strings.TrimSpace(since)
	if since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			return fmt.Errorf("--since must be an RFC3339 timestamp: %w", err)
		}
	}
	workspaceID, err := resolveWorkspaceArg(cmd, args)
	if err != nil {
		return err
	}
	if workspaceID == "" {
		return fmt.Errorf("workspace ID is required: pass an id/slug/prefix as argument or set MULTICA_WORKSPACE_ID")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	path := "/api/workspaces/" + url.PathEscape(workspaceID) + "/memory-comparison"
	if since != "" {
		query := url.Values{}
		query.Set("since", since)
		path += "?" + query.Encode()
	}
	var comparison memoryComparisonResponse
	if err := client.GetJSON(ctx, path, &comparison); err != nil {
		return fmt.Errorf("read memory comparison: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, comparison)
	}
	if output != "table" {
		return fmt.Errorf("invalid output %q; valid values: table, json", output)
	}
	return printMemoryComparison(os.Stdout, comparison, since)
}

func printMemoryComparison(w io.Writer, comparison memoryComparisonResponse, since string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "WORKSPACE\t%s\n", comparison.WorkspaceID)
	if since != "" {
		fmt.Fprintf(tw, "SINCE\t%s\n", since)
	}
	fmt.Fprintf(tw, "RATED RUNS\t%d\n", comparison.Marked)
	fmt.Fprintf(tw, "PREFERENCE\tmoct=%d%% powercontext=%d%% neither=%d%%\n",
		comparison.PreferencePct.Moct, comparison.PreferencePct.PowerContext, comparison.PreferencePct.Neither)
	fmt.Fprintf(tw, "VOTES\tmoct=%d powercontext=%d neither=%d\n",
		comparison.Votes.Moct, comparison.Votes.PowerContext, comparison.Votes.Neither)
	fmt.Fprintf(tw, "SHADOW\twith_block=%d without_block=%d\n",
		comparison.ShadowStatus.WithBlock, comparison.ShadowStatus.WithoutBlock)
	fmt.Fprintln(tw, "BACKEND\tBYTES\tLATENCY_MS")
	for _, backend := range comparison.Backends {
		fmt.Fprintf(tw, "%s\t%d\t%d\n", backend.Backend, backend.Bytes, backend.LatencyMS)
	}
	return tw.Flush()
}
