package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/moct"
	"github.com/multica-ai/multica/server/internal/moctmcp"
)

// Environment this subcommand reads. The Multica daemon writes them into the
// MCP server entry it adds to a task's config (docs/moct-seam.md), and that
// document fixes the names: they are the seam contract, and none of them is a
// tool argument — a run cannot ask for memory outside its own scope.
const (
	moctBaseURLEnv     = "MOCT_BASE_URL"
	moctWorkspaceIDEnv = "MOCT_WORKSPACE_ID"
	moctExpertIDEnv    = "MOCT_EXPERT_ID"
	moctTaskRefEnv     = "MOCT_TASK_REF"
	moctChannelRefEnv  = "MOCT_CHANNEL_REF"
)

var moctMCPCmd = &cobra.Command{
	Use:   "moct-mcp",
	Short: "Serve the MOCT memory tools (memory_search, memory_get, memory_note) on stdio",
	Long: `Serve the MOCT team-memory tools over the MCP stdio protocol.

Machine-facing. The Multica daemon starts this inside a task and mounts it as
the "moct" MCP server; it is not meant to be run by hand. It takes no
arguments and speaks JSON-RPC on stdin/stdout, so everything it needs arrives
as environment the daemon sets: MOCT_BASE_URL, MOCT_WORKSPACE_ID,
MOCT_EXPERT_ID, MOCT_TASK_REF and MOCT_CHANNEL_REF. moct-be stays the only
writer to the memory store — this is a pure HTTP client in front of it.

A moct-be that is down, unconfigured or unparseable makes every tool call
answer with a tool error while this process stays alive: a memory backend must
never take a task down with it (docs/moct-seam.md, failure mode).`,
	Args: cobra.NoArgs,
	RunE: runMoctMCP,
}

func runMoctMCP(cmd *cobra.Command, _ []string) error {
	scope := moctmcp.Scope{
		WorkspaceRef: strings.TrimSpace(os.Getenv(moctWorkspaceIDEnv)),
		ExpertRef:    strings.TrimSpace(os.Getenv(moctExpertIDEnv)),
		TaskRef:      strings.TrimSpace(os.Getenv(moctTaskRefEnv)),
		ChannelRef:   strings.TrimSpace(os.Getenv(moctChannelRefEnv)),
	}
	// stdout belongs to the protocol, so every human-readable line goes to
	// stderr — a stray byte on stdout would corrupt the JSON-RPC stream.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	baseURL := strings.TrimSpace(os.Getenv(moctBaseURLEnv))
	var client *moct.Client
	switch {
	case baseURL == "":
		logger.Warn("moct-mcp: " + moctBaseURLEnv + " is not set; every tool call will fail")
	default:
		c, err := moct.NewClient(moct.Config{BaseURL: baseURL})
		if err != nil {
			// Serve anyway. Exiting would fail the session that spawned us.
			logger.Warn("moct-mcp: "+moctBaseURLEnv+" is not usable; every tool call will fail",
				"base_url", baseURL, "error", err)
		} else {
			client = c
		}
	}
	return moctmcp.New(client, scope, logger).Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
}
