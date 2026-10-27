package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/askplanmcp"
)

// Environment this subcommand reads. The Multica daemon writes them into the
// MCP server entry it adds to a task's config (S1-A2, M-119), and none of
// them is a tool argument — a run cannot ask/plan outside its own task.
const (
	askPlanBaseURLEnv   = "MULTICA_SERVER_URL"
	askPlanTokenEnv     = "MULTICA_TOKEN"
	askPlanWorkspaceEnv = "MULTICA_WORKSPACE_ID"
	askPlanTaskEnv      = "MULTICA_TASK_ID"
)

var askPlanMCPCmd = &cobra.Command{
	Use:   "askplan-mcp",
	Short: "Serve the agent ask/plan tools (ask, plan) on stdio",
	Long: `Serve the Multica ask/plan tools over the MCP stdio protocol.

Machine-facing. The Multica daemon starts this inside a task and mounts it as
the "multica" MCP server; it is not meant to be run by hand. It takes no
arguments and speaks JSON-RPC on stdin/stdout, so everything it needs arrives
as environment the daemon sets: MULTICA_SERVER_URL, MULTICA_TOKEN (the
task-scoped mat_ token), MULTICA_WORKSPACE_ID and MULTICA_TASK_ID. The tools
call multica-be's ask/plan routes only — never MOCT.

A call returns immediately: the server parks the task as blocked ("awaiting
human") and the answer or verdict re-queues a run resuming the same session.
A failure (bad arguments, or multica-be's structured 409 with the current
revision) is a TOOL error while this process stays alive — the daemon never
adds this entry when the task cannot use ask/plan, and an MCP server that
dies would fail the surrounding ACP/dsh session.`,
	Args: cobra.NoArgs,
	RunE: runAskPlanMCP,
}

func runAskPlanMCP(cmd *cobra.Command, _ []string) error {
	scope := askplanmcp.Scope{
		WorkspaceID: strings.TrimSpace(os.Getenv(askPlanWorkspaceEnv)),
		TaskID:      strings.TrimSpace(os.Getenv(askPlanTaskEnv)),
	}
	// stdout belongs to the protocol, so every human-readable line goes to
	// stderr — a stray byte on stdout would corrupt the JSON-RPC stream.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	client, err := askplanmcp.NewAPIClient(os.Getenv(askPlanBaseURLEnv), os.Getenv(askPlanTokenEnv))
	if err != nil {
		// Serve anyway. Exiting would fail the session that spawned us; the
		// missing configuration surfaces on each tool call instead.
		logger.Warn("askplan-mcp: configuration unusable; every tool call will fail", "error", err)
		client = nil
	}
	return askplanmcp.New(client, scope, logger).Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
}
