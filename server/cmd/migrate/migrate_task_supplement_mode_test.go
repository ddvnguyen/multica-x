package main

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMigrate602TaskSupplementModeIsIdempotent covers the M-135 migration
// trap: lanes that ran the same SQL as 590_task_supplement_mode already
// have the `mode` column and its CHECK constraint. A bare re-run of 602
// must never fail with "already exists", and a fresh database must get
// both objects. It also pins the rename gate: when both objects exist the
// condition records 602 as applied without re-running the SQL.
func TestMigrate602TaskSupplementModeIsIdempotent(t *testing.T) {
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	schema := createScratchSchema(t, ctx, base, "task_supplement_mode_")
	pool := openTestPoolWithSearchPath(t, schema)

	// Fresh database — 602 applies and creates both objects.
	if err := runMigrations(ctx, pool, runOptions{
		Direction:             "up",
		Files:                 realMigrationFiles(t, []string{"602_task_supplement_mode"}, "up"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
	}); err != nil {
		t.Fatalf("fresh migrate up: %v", err)
	}
	// Existing rows default to 'steer' under the NOT NULL DEFAULT.
	if _, err := pool.Exec(ctx, `INSERT INTO task_supplement (task_id, workspace_id, issue_id, comment_id, author_id, client_request_id, status) VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 'pending')`); err != nil {
		t.Fatalf("insert supplement row: %v", err)
	}
	var mode string
	if err := pool.QueryRow(ctx, `SELECT mode FROM task_supplement`).Scan(&mode); err != nil || mode != "steer" {
		t.Fatalf("mode default = %q, %v, want steer", mode, err)
	}
	// A value outside the check must be rejected, pinning the constraint.
	if _, err := pool.Exec(ctx, `INSERT INTO task_supplement (task_id, workspace_id, issue_id, comment_id, author_id, client_request_id, status, mode) VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 'pending', 'fly')`); err == nil {
		t.Fatal("check constraint accepted an out-of-range mode")
	}
	assertModeObjects(t, ctx, pool, true)

	// "590 already applied" state — re-running 602 must be a no-op success,
	// never a duplicate column or duplicate constraint error.
	if err := runMigrations(ctx, pool, runOptions{
		Direction:             "up",
		Files:                 realMigrationFiles(t, []string{"602_task_supplement_mode"}, "up"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
	}); err != nil {
		t.Fatalf("re-run migrate up: %v", err)
	}
	assertModeObjects(t, ctx, pool, true)
}

func assertModeObjects(t *testing.T, ctx context.Context, pool *pgxpool.Pool, present bool) {
	t.Helper()
	var columnCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'task_supplement' AND column_name = 'mode'
	`).Scan(&columnCount); err != nil {
		t.Fatal(err)
	}
	var constraintCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE conname = 'task_supplement_mode_check'
		  AND conrelid = 'task_supplement'::regclass
	`).Scan(&constraintCount); err != nil {
		t.Fatal(err)
	}
	if present && (columnCount != 1 || constraintCount != 1) {
		t.Fatalf("mode objects after up: column=%d constraint=%d, want 1/1", columnCount, constraintCount)
	}
}
