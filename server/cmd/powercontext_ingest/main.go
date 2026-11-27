// powercontext_ingest mirrors a workspace's curated records into its
// PowerContext plane: current issue decisions and human-authored issue
// comments. It is the backfill half of the memory dogfood (M-616 item 3) — the
// seam writes each run's handoff, this fills in the knowledge that predates it.
//
// Run it repeatedly: entries are skipped when their marker is already in the
// scope, so a second pass writes nothing new. It never writes agent comments and
// never mirrors MOCT records (no list endpoint exists yet); both are documented
// in docs/powercontext-seam.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/powercontext"
	"github.com/multica-ai/multica/server/internal/powercontextingest"
)

func main() {
	logger.Init()
	if err := run(); err != nil {
		slog.Error("powercontext ingest failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	workspaceID := flag.String("workspace", "", "workspace id to mirror (required)")
	sinceRaw := flag.String("since", "", "only records created at or after this RFC3339 time (default: all history)")
	limit := flag.Int("limit", powercontextingest.DefaultLimit, "maximum rows read per source")
	dryRun := flag.Bool("dry-run", false, "read and report what would be written without writing it")
	flag.Parse()

	workspace := strings.TrimSpace(*workspaceID)
	if workspace == "" {
		return fmt.Errorf("--workspace is required")
	}
	if *limit < 1 {
		return fmt.Errorf("--limit must be at least 1")
	}
	var since time.Time
	if raw := strings.TrimSpace(*sinceRaw); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return fmt.Errorf("--since must be RFC3339: %w", err)
		}
		since = parsed
	}

	baseURL := strings.TrimSpace(os.Getenv("POWERCONTEXT_BASE_URL"))
	if baseURL == "" && !*dryRun {
		return fmt.Errorf("POWERCONTEXT_BASE_URL is required (the mirror has no primary switch: MOCT is not a write target here)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	entries, err := powercontextingest.Read(ctx, pool, powercontextingest.Options{
		WorkspaceID: workspace,
		Since:       since,
		Limit:       *limit,
	})
	if err != nil {
		return err
	}
	slog.Info("powercontext ingest read", "workspace", workspace, "entries", len(entries), "dry_run", *dryRun)

	if *dryRun {
		for _, e := range entries {
			scope, ok := powercontextingest.Scope(e)
			if !ok {
				scope = "(no scope)"
			}
			fmt.Printf("%s\t%s\t%s\n", powercontextingest.Marker(e), scope, powercontextingest.PowerContextKind(e))
		}
		fmt.Printf("would write %d entries\n", len(entries))
		return nil
	}

	client, err := powercontext.NewClient(powercontext.Config{
		BaseURL: baseURL,
		Token:   strings.TrimSpace(os.Getenv("POWERCONTEXT_TOKEN")),
	})
	if err != nil {
		return err
	}

	summary, err := powercontextingest.Ingest(ctx, client, entries)
	if err != nil {
		return err
	}
	slog.Info("powercontext ingest finished",
		"workspace", workspace,
		"decisions", summary.Decisions,
		"comments", summary.Comments,
		"written", summary.Written,
		"skipped", summary.Skipped,
		"failed", summary.Failed,
	)
	for _, failure := range summary.Failures {
		slog.Warn("powercontext ingest entry failed", "entry", failure)
	}
	if summary.Failed > 0 {
		// Non-zero exit so a scheduled run is visible; the pass is safe to
		// repeat, and entries that failed were not written.
		return fmt.Errorf("%d of %d entries failed", summary.Failed, len(entries))
	}
	return nil
}
