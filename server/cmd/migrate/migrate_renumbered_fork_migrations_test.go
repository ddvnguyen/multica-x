// M-167: the lane databases recorded the fork's own migrations under the
// pre-v0.7.1 numbers; the sync renumbered the identical SQL into 596-602, so
// a lane run finds the objects but not the version rows. Each renumbered
// migration is gated on the objects it ships: skip-and-record only when every
// one of them is present, apply the SQL otherwise so a fresh database still
// runs it.
package main

import (
	"context"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// renumberedForkVersions lists the gated migrations in apply order. 599 is
// deliberately absent: its DROP + ADD CONSTRAINT sequence replays cleanly on
// objects that already exist.
func renumberedForkVersions() []string {
	return []string{
		"596_agent_question_card",
		"597_agent_plan",
		"598_agent_plan_review",
		"600_project_settings",
		"601_workspace_account",
		"602_task_supplement_mode",
	}
}

// renumberedForkRequirements maps every gated version to the requirement that
// gates it, so the tests can walk the exact wiring the real run uses.
func renumberedForkRequirements() map[string]objectRequirement {
	return map[string]objectRequirement{
		"596_agent_question_card":  migr596AgentQuestionCard,
		"597_agent_plan":           migr597AgentPlan,
		"598_agent_plan_review":    migr598AgentPlanReview,
		"600_project_settings":     migr600ProjectSettings,
		"601_workspace_account":    migr601WorkspaceAccount,
		"602_task_supplement_mode": migr602TaskSupplementMode,
	}
}

// applyRenumberedForkFixture runs the real migration SQL by hand, mimicking
// the pre-renumbering application that left a lane database with the objects
// but without the renumbered version rows.
func applyRenumberedForkFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, version := range renumberedForkVersions() {
		for _, path := range realMigrationFiles(t, []string{version}, "up") {
			sql, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if _, err := pool.Exec(ctx, string(sql)); err != nil {
				t.Fatalf("execute %s: %v", path, err)
			}
		}
	}
}

// createRenumberedForkPrerequisites builds the minimal tables the gated
// migrations reference, matching the schema the real chain has in place long
// before 596-602 become reachable.
func createRenumberedForkPrerequisites(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE "user" (id UUID PRIMARY KEY)`,
		`CREATE TABLE workspace (id UUID PRIMARY KEY)`,
		`CREATE TABLE project (id UUID PRIMARY KEY)`,
		`CREATE TABLE agent (id UUID PRIMARY KEY)`,
		`CREATE TABLE agent_task_queue (id UUID PRIMARY KEY)`,
		`CREATE TABLE task_supplement (id UUID PRIMARY KEY)`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("create renumbered fork prerequisite: %v (%s)", err, statement)
		}
	}
}

// evaluateRenumberedForkCondition runs one gate with its own connection and
// the search_path the scratch schema pins, the way runMigrations does.
func evaluateRenumberedForkCondition(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	version string,
) (bool, string) {
	t.Helper()
	condition, ok := upMigrationConditions[version]
	if !ok || condition == nil {
		t.Fatalf("upMigrationConditions[%q] is not wired; the run would re-execute the migration", version)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection for %s: %v", version, err)
	}
	defer conn.Release()
	apply, reason, err := condition(ctx, conn)
	if err != nil {
		t.Fatalf("evaluate gate for %s: %v", version, err)
	}
	return apply, reason
}

func assertRenumberedForkGate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version string, wantApply bool) {
	t.Helper()
	apply, reason := evaluateRenumberedForkCondition(t, ctx, pool, version)
	if apply != wantApply {
		t.Fatalf("gate for %s = apply %v (reason %q), want apply %v", version, apply, reason, wantApply)
	}
	if !wantApply && reason == "" {
		t.Fatalf("gate for %s skipped the SQL without recording why", version)
	}
}

// assertRenumberedForkObjectsExist counts every object a gated version ships
// in schema, so a fresh-database run cannot claim success while leaving an
// index or constraint behind.
func assertRenumberedForkObjectsExist(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	schema string,
	requirements map[string]objectRequirement,
) {
	t.Helper()
	for _, version := range renumberedForkVersions() {
		requirement := requirements[version]
		want := len(requirement.Tables) + len(requirement.Indexes) +
			len(requirement.Columns) + len(requirement.Constraints)
		if want == 0 {
			t.Fatalf("requirement for %s checks no objects", version)
		}
		objects := 0
		for _, object := range requirement.Tables {
			objects += countScratchRegclass(t, ctx, pool, schema, object)
		}
		for _, object := range requirement.Indexes {
			objects += countScratchRegclass(t, ctx, pool, schema, object)
		}
		for _, object := range requirement.Columns {
			objects += countScratchColumn(t, ctx, pool, schema, object)
		}
		for _, constraint := range requirement.Constraints {
			objects += countScratchConstraint(t, ctx, pool, schema, constraint)
		}
		if objects != want {
			t.Fatalf("after fresh apply of %s, %d of %d gated objects exist in %s", version, objects, want, schema)
		}
	}
}

func countScratchRegclass(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string, name string) int {
	t.Helper()
	return countScratch(t, ctx, pool,
		`SELECT COUNT(*) FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2`,
		schema, name)
}

func countScratchColumn(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string, column string) int {
	t.Helper()
	table, col, found := strings.Cut(column, ".")
	if !found {
		t.Fatalf("invalid column fixture reference %q", column)
	}
	return countScratch(t, ctx, pool,
		`SELECT COUNT(*) FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`,
		schema, table, col)
}

func countScratchConstraint(t *testing.T, ctx context.Context, pool *pgxpool.Pool, schema string, requirement constraintRequirement) int {
	t.Helper()
	return countScratch(t, ctx, pool,
		`SELECT COUNT(*) FROM pg_constraint c
		 JOIN pg_class rel ON rel.oid = c.conrelid
		 JOIN pg_namespace n ON n.oid = rel.relnamespace
		 WHERE n.nspname = $1 AND rel.relname = $2 AND c.conname = $3 AND c.contype = $4::"char"`,
		schema, requirement.Table, requirement.Name, requirement.Kind)
}

func countScratch(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count objects with %q: %v", query, err)
	}
	return count
}

// TestRenumberedForkMigrationGates runs every gate against an empty scratch
// schema, then against the same schema re-created by the real migration SQL:
// absent objects must apply the SQL, present objects must skip it with a
// reason.
func TestRenumberedForkMigrationGates(t *testing.T) {
	t.Parallel()
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	schema := createScratchSchema(t, ctx, base, "renumbered_fork_gates_")
	pool := openTestPoolWithSearchPath(t, schema)

	for _, version := range renumberedForkVersions() {
		assertRenumberedForkGate(t, ctx, pool, version, true)
	}

	createRenumberedForkPrerequisites(t, ctx, pool)
	applyRenumberedForkFixture(t, ctx, pool)
	for _, version := range renumberedForkVersions() {
		assertRenumberedForkGate(t, ctx, pool, version, false)
	}

	// A lane database with a same-named table but a missing index must not be
	// recorded as migrated: the gate fails open and the migration's own DDL
	// then rejects the drift loudly (M-167).
	if _, err := pool.Exec(ctx, `DROP INDEX idx_workspace_account_workspace`); err != nil {
		t.Fatalf("drop fixture index: %v", err)
	}
	assertRenumberedForkGate(t, ctx, pool, "601_workspace_account", true)
	if _, err := pool.Exec(ctx, `CREATE INDEX idx_workspace_account_workspace ON workspace_account(workspace_id)`); err != nil {
		t.Fatalf("restore fixture index: %v", err)
	}
	assertRenumberedForkGate(t, ctx, pool, "601_workspace_account", false)

	// Constraint drift behaves the same: the kind matters, not just the name.
	if _, err := pool.Exec(ctx, `ALTER TABLE task_supplement DROP CONSTRAINT task_supplement_mode_check`); err != nil {
		t.Fatalf("drop fixture check: %v", err)
	}
	assertRenumberedForkGate(t, ctx, pool, "602_task_supplement_mode", true)
	if _, err := pool.Exec(ctx, `ALTER TABLE task_supplement ADD CONSTRAINT task_supplement_mode_check CHECK (mode IN ('steer','queue'))`); err != nil {
		t.Fatalf("restore fixture check: %v", err)
	}
	assertRenumberedForkGate(t, ctx, pool, "602_task_supplement_mode", false)
}

// TestRenumberedForkMigrationsLaneDatabase replays the lane state inside one
// scratch schema: the objects exist through the pre-renumbering application,
// no version rows do. runMigrations must record every renumbered version
// without executing its SQL — the ungated run would abort with SQLSTATE 42P07
// — and a second run must see them as already applied.
func TestRenumberedForkMigrationsLaneDatabase(t *testing.T) {
	t.Parallel()
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	schema := createScratchSchema(t, ctx, base, "renumbered_fork_lane_")
	pool := openTestPoolWithSearchPath(t, schema)
	createRenumberedForkPrerequisites(t, ctx, pool)
	applyRenumberedForkFixture(t, ctx, pool)

	versions := renumberedForkVersions()
	options := runOptions{
		Direction:             "up",
		Files:                 realMigrationFiles(t, versions, "up"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
		Hooks:                 hooksForDirection("up"),
		Conditions:            conditionsForDirection("up"),
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("apply renumbered fork migrations over a lane-like database: %v", err)
	}
	for _, version := range versions {
		assertMigrationVersionRecorded(t, ctx, pool, schema, version, true)
	}

	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("repeat renumbered fork migrations over a lane-like database: %v", err)
	}
	for _, version := range versions {
		assertMigrationVersionRecorded(t, ctx, pool, schema, version, true)
	}
}

// TestRenumberedForkMigrationsFreshDatabase proves the gates stay
// apply-by-default: on a scratch schema holding only the prerequisites the run
// executes every renumbered migration and leaves each gated object behind,
// then re-runs cleanly.
func TestRenumberedForkMigrationsFreshDatabase(t *testing.T) {
	t.Parallel()
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	schema := createScratchSchema(t, ctx, base, "renumbered_fork_fresh_")
	pool := openTestPoolWithSearchPath(t, schema)
	createRenumberedForkPrerequisites(t, ctx, pool)

	versions := renumberedForkVersions()
	options := runOptions{
		Direction:             "up",
		Files:                 realMigrationFiles(t, versions, "up"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
		Hooks:                 hooksForDirection("up"),
		Conditions:            conditionsForDirection("up"),
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("apply renumbered fork migrations on a fresh database: %v", err)
	}
	assertRenumberedForkObjectsExist(t, ctx, pool, schema, renumberedForkRequirements())
	for _, version := range versions {
		assertMigrationVersionRecorded(t, ctx, pool, schema, version, true)
		assertRenumberedForkGate(t, ctx, pool, version, false)
	}

	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("repeat renumbered fork migrations on a fresh database: %v", err)
	}
}
