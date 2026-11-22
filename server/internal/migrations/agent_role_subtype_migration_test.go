package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAgentRoleSubtypeMigrationRoundtrip exercises the additive up migration
// and the guard on its down migration. The down path must refuse to delete a
// live Role or a live Agent->Role link, and must succeed once those rows are
// gone. No foreign keys back the link, so this guard is the rollback fence.
//
// A temp table is enough: the migration's DO blocks scope their pg_constraint
// lookups to `agent` in the current search_path, so the real table's
// constraints cannot make them skip the new ones.
func TestAgentRoleSubtypeMigrationRoundtrip(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire Postgres connection: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `
		CREATE TEMP TABLE agent (
			id UUID PRIMARY KEY,
			workspace_id UUID NOT NULL,
			kind TEXT NOT NULL DEFAULT 'user'
		)
	`); err != nil {
		t.Fatalf("create temporary agent table: %v", err)
	}
	applyMigrationFile(t, ctx, conn.Conn(), "747_agent_role_subtype.up.sql")

	// The default shape is the legacy Agent; Role-only columns are Role-scoped
	// and the CHECKs backstop the API validations.
	if _, err := conn.Exec(ctx, `INSERT INTO agent (id, workspace_id) VALUES ($1, $2)`,
		"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000010"); err != nil {
		t.Fatalf("insert legacy agent row: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO agent (id, workspace_id, subtype, tier_role) VALUES ($1, $2, 'role', 'architect')`,
		"00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000010"); err != nil {
		t.Fatalf("insert role row: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE agent SET implements_role_id = $2 WHERE id = $1`,
		"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"); err != nil {
		t.Fatalf("link agent to role: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE agent SET tier_role = 'engineer' WHERE id = $1`,
		"00000000-0000-0000-0000-000000000001"); err == nil {
		t.Fatal("tier_role on an ordinary Agent was accepted, want CHECK violation")
	}
	if _, err := conn.Exec(ctx, `UPDATE agent SET implements_role_id = $2 WHERE id = $1`,
		"00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000002"); err == nil {
		t.Fatal("a Role linking to another Role was accepted, want CHECK violation")
	}

	down := readMigrationFile(t, "747_agent_role_subtype.down.sql")
	if _, err := conn.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "M-501 down aborted") {
		t.Fatalf("down migration with a live Role: got %v, want abort", err)
	}

	if _, err := conn.Exec(ctx, `DELETE FROM agent WHERE id <> $1`,
		"00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("remove role rows: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE agent SET implements_role_id = NULL WHERE id = $1`,
		"00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("unlink agent: %v", err)
	}
	if _, err := conn.Exec(ctx, down); err != nil {
		t.Fatalf("down migration after removing Roles: %v", err)
	}
	var subtype string
	if err := conn.QueryRow(ctx, `SELECT subtype FROM agent LIMIT 1`).Scan(&subtype); err == nil {
		t.Fatalf("subtype column still readable after down migration: %q", subtype)
	}
}
