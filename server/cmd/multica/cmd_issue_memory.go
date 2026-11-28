package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// presentString records whether a JSON string field carried text without
// keeping the text in the output structs. Memory blocks can be large; these
// commands print their measurements and their status, never the injected or
// shadow body. The decoded value is discarded inside UnmarshalJSON, so a later
// change that prints the whole struct cannot print the memory body.
type presentString bool

func (p *presentString) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = presentString(value != "")
	return nil
}

// memoryPartResponse is only the fields of a run's memory part the CLI may
// print. `block` and `entries` are deliberately absent: the JSON decoder drops
// them, so the memory body never reaches the output structs below.
type memoryPartResponse struct {
	Backend   string              `json:"backend"`
	Bytes     int                 `json:"bytes"`
	Tokens    int                 `json:"tokens"`
	LatencyMS int64               `json:"latency_ms"`
	Note      string              `json:"note"`
	Error     string              `json:"error"`
	HasBlock  presentString       `json:"block"`
	Shadow    *memoryPartResponse `json:"shadow"`
}

type taskMemoryReportResponse struct {
	TaskID      string              `json:"task_id"`
	WorkspaceID string              `json:"workspace_id"`
	Memory      *memoryPartResponse `json:"memory"`
	Mark        *memoryMarkView     `json:"mark"`
}

// memoryBlockView is one side of a comparison: its measurements and why it
// produced no block, but never the block itself.
type memoryBlockView struct {
	Backend   string `json:"backend"`
	Status    string `json:"status"`
	Bytes     int    `json:"bytes"`
	Tokens    int    `json:"tokens"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
	Note      string `json:"note,omitempty"`
}

// memoryMarkView is a run's saved vote. The server fills the numbers from the
// run row, so a client cannot restate what the reader saw.
type memoryMarkView struct {
	TaskID           string `json:"task_id"`
	Choice           string `json:"choice"`
	MarkedByType     string `json:"marked_by_type"`
	PrimaryBytes     int32  `json:"primary_bytes"`
	PrimaryLatencyMS int32  `json:"primary_latency_ms"`
	ShadowBytes      int32  `json:"shadow_bytes"`
	ShadowLatencyMS  int32  `json:"shadow_latency_ms"`
	MarkedAt         string `json:"marked_at"`
}

type memoryReportView struct {
	TaskID      string           `json:"task_id"`
	WorkspaceID string           `json:"workspace_id"`
	Injected    memoryBlockView  `json:"injected"`
	Shadow      *memoryBlockView `json:"shadow"`
	Mark        *memoryMarkView  `json:"mark"`
}

// memoryBlockViewOf labels a side by what it produced. "block" is a comparison
// that happened; "error" and "note" are the recorded reasons it did not, and
// "absent" is an older daemon that never read a second backend.
func memoryBlockViewOf(part *memoryPartResponse) memoryBlockView {
	if part == nil {
		return memoryBlockView{Status: "absent"}
	}
	view := memoryBlockView{
		Backend:   part.Backend,
		Bytes:     part.Bytes,
		Tokens:    part.Tokens,
		LatencyMS: part.LatencyMS,
		Error:     part.Error,
		Note:      part.Note,
	}
	switch {
	case bool(part.HasBlock):
		view.Status = "block"
	case part.Error != "":
		view.Status = "error"
	case part.Note != "":
		view.Status = "note"
	default:
		view.Status = "empty"
	}
	return view
}

var issueMemoryReportCmd = &cobra.Command{
	Use:   "memory-report <issue> <task>",
	Short: "Read a run's memory comparison measurements (never the memory body)",
	Long: "Prints the injected and shadow memory blocks' backend, status, bytes, tokens and latency, " +
		"plus the run's current mark if one was saved. The blocks themselves are never printed: " +
		"an agent rating a run needs the measurements, not a second copy of its prompt context.\n\n" +
		"The task accepts a full run UUID or a short prefix; use `multica issue runs <issue> --full-id` to copy one.",
	Args: exactArgs(2),
	RunE: runIssueMemoryReport,
}

var issueMemoryMarkCmd = &cobra.Command{
	Use:   "memory-mark <issue> <task> --choice <value>",
	Short: "Save which memory block this run's reader found more useful",
	Long: "Records one vote per run on the M-613 memory comparison. Valid choices are " +
		"moct, powercontext and neither; voting again replaces the earlier vote. The server " +
		"re-reads the run's own measurements, so no sizes are accepted from the caller. " +
		"Only a run with a recorded shadow block can be marked.\n\n" +
		"Every mark counts in the M-613 tally: do not use this command to test the CLI. " +
		"The tests use a local HTTP server instead.",
	Args: exactArgs(2),
	RunE: runIssueMemoryMark,
}

func init() {
	issueMemoryReportCmd.Flags().String("output", "table", "Output format: table or json")
	issueMemoryMarkCmd.Flags().String("choice", "", "Which block was more useful: moct, powercontext or neither")
	issueMemoryMarkCmd.Flags().String("output", "table", "Output format: table or json")
	_ = issueMemoryMarkCmd.MarkFlagRequired("choice")
	issueCmd.AddCommand(issueMemoryReportCmd)
	issueCmd.AddCommand(issueMemoryMarkCmd)
}

func runIssueMemoryReport(cmd *cobra.Command, args []string) error {
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
	taskRef, err := resolveTaskRunID(ctx, client, issueRef.ID, args[1])
	if err != nil {
		return fmt.Errorf("resolve task run: %w", err)
	}
	path := "/api/issues/" + url.PathEscape(issueRef.ID) + "/tasks/" + url.PathEscape(taskRef.ID) + "/memory-report"
	var report taskMemoryReportResponse
	if err := client.GetJSON(ctx, path, &report); err != nil {
		return fmt.Errorf("read memory report: %w", err)
	}
	view := memoryReportView{
		TaskID:      report.TaskID,
		WorkspaceID: report.WorkspaceID,
		Injected:    memoryBlockViewOf(report.Memory),
		Mark:        report.Mark,
	}
	if report.Memory != nil && report.Memory.Shadow != nil {
		shadow := memoryBlockViewOf(report.Memory.Shadow)
		view.Shadow = &shadow
	}
	return printMemoryReport(cmd, view)
}

func printMemoryReport(cmd *cobra.Command, view memoryReportView) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, view)
	}
	if output != "table" {
		return fmt.Errorf("invalid output %q; valid values: table, json", output)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SIDE\tBACKEND\tSTATUS\tBYTES\tTOKENS\tLATENCY_MS\tDETAIL")
	fmt.Fprintf(w, "injected\t%s\t%s\t%d\t%d\t%d\t%s\n",
		view.Injected.Backend, view.Injected.Status, view.Injected.Bytes, view.Injected.Tokens,
		view.Injected.LatencyMS, memoryBlockDetail(view.Injected))
	shadow := memoryBlockView{Status: "absent"}
	if view.Shadow != nil {
		shadow = *view.Shadow
	}
	fmt.Fprintf(w, "shadow\t%s\t%s\t%d\t%d\t%d\t%s\n",
		shadow.Backend, shadow.Status, shadow.Bytes, shadow.Tokens, shadow.LatencyMS, memoryBlockDetail(shadow))
	if err := w.Flush(); err != nil {
		return err
	}
	if view.Mark == nil {
		fmt.Fprintln(os.Stdout, "mark: none")
		return nil
	}
	mark := view.Mark
	fmt.Fprintf(os.Stdout, "mark: choice=%s by=%s at=%s primary=%dB/%dms shadow=%dB/%dms\n",
		mark.Choice, mark.MarkedByType, mark.MarkedAt,
		mark.PrimaryBytes, mark.PrimaryLatencyMS, mark.ShadowBytes, mark.ShadowLatencyMS)
	return nil
}

func memoryBlockDetail(block memoryBlockView) string {
	if block.Error != "" {
		return block.Error
	}
	if block.Note != "" {
		return block.Note
	}
	return "-"
}

func runIssueMemoryMark(cmd *cobra.Command, args []string) error {
	choice, _ := cmd.Flags().GetString("choice")
	choice = strings.TrimSpace(choice)
	switch choice {
	case "moct", "powercontext", "neither":
	default:
		return fmt.Errorf("choice must be moct, powercontext or neither (got %q)", choice)
	}
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
	taskRef, err := resolveTaskRunID(ctx, client, issueRef.ID, args[1])
	if err != nil {
		return fmt.Errorf("resolve task run: %w", err)
	}
	path := "/api/issues/" + url.PathEscape(issueRef.ID) + "/tasks/" + url.PathEscape(taskRef.ID) + "/memory-mark"
	var mark memoryMarkView
	if err := client.PostJSON(ctx, path, map[string]any{"choice": choice}, &mark); err != nil {
		return fmt.Errorf("save memory mark: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, mark)
	}
	if output != "table" {
		return fmt.Errorf("invalid output %q; valid values: table, json", output)
	}
	fmt.Fprintf(os.Stdout, "Saved: task=%s choice=%s by=%s at=%s\n",
		mark.TaskID, mark.Choice, mark.MarkedByType, mark.MarkedAt)
	return nil
}
