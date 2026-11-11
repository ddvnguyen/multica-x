package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// finalStemOf returns the newest stem for one migration name, which is what a
// renumbered row must end on. Two generations target the same name (595 and 617
// both resolve to the 637 stem), so the newest number wins.
func finalStemOf(name string) string {
	final := ""
	for _, pair := range forkMigrationRenames {
		_, rest, _ := strings.Cut(pair[1], "_")
		if rest == name && (final == "" || migrationNumber(pair[1]) > migrationNumber(final)) {
			final = pair[1]
		}
	}
	return final
}

// TestForkMigrationRenamesMatchFiles pins the renumber table to the tree: every
// new stem must be a migration that exists, every old stem must be gone, and the
// shift must be one this fork has documented in forkMigrationRenumberShifts,
// applied in ascending order. A rename that names a stem no file ever had is a
// silent no-op on exactly the databases the hook exists for, so the table cannot
// be trusted unchecked.
func TestForkMigrationRenamesMatchFiles(t *testing.T) {
	t.Parallel()
	if len(forkMigrationRenames) == 0 {
		t.Fatal("no fork migration renames registered")
	}
	oldStems := make(map[string]bool, len(forkMigrationRenames))
	prevNew, prevShift := 0, 0
	for _, pair := range forkMigrationRenames {
		oldVersion, newVersion := pair[0], pair[1]
		if _, err := os.Stat(filepath.Join("..", "..", "migrations", newVersion+".up.sql")); err != nil {
			t.Errorf("new stem %q has no up migration: %v", newVersion, err)
		}
		if _, err := os.Stat(filepath.Join("..", "..", "migrations", oldVersion+".up.sql")); err == nil {
			t.Errorf("old stem %q still exists on disk; the rename is done in the tree", oldVersion)
		}
		if oldStems[oldVersion] {
			t.Errorf("old stem %q is renamed twice", oldVersion)
		}
		oldStems[oldVersion] = true
		oldNum, err := strconv.Atoi(strings.SplitN(oldVersion, "_", 2)[0])
		if err != nil {
			t.Fatalf("old stem %q is not numbered: %v", oldVersion, err)
		}
		newNum, err := strconv.Atoi(strings.SplitN(newVersion, "_", 2)[0])
		if err != nil {
			t.Fatalf("new stem %q is not numbered: %v", newVersion, err)
		}
		// Order is preserved within a generation; a generation boundary (a
		// different shift) starts its numbering over, which is why the collapsed
		// 595 -> 637 block and the 617 -> 637 block overlap in target numbers.
		if shift := newNum - oldNum; shift == prevShift && newNum <= prevNew {
			t.Errorf("%q -> %q breaks the preserved order", oldVersion, newVersion)
		}
		prevNew, prevShift = newNum, newNum-oldNum
		oldName := strings.TrimPrefix(oldVersion, strconv.Itoa(oldNum)+"_")
		newName := strings.TrimPrefix(newVersion, strconv.Itoa(newNum)+"_")
		if oldName != newName {
			t.Errorf("%q -> %q changed the migration name, not just its number", oldVersion, newVersion)
		}
		if want := finalStemOf(newName); newVersion != want {
			t.Errorf("%q -> %q is not the newest stem for %q (%q)", oldVersion, newVersion, newName, want)
		}
	}
}

// TestForkMigrationRenameOrderChainsGenerations is the guard for the hazard the
// application order exists for. The table chains: v0.8.1 moved 595-stems to 617,
// v0.8.2 moves 617-stems to 637, and 637 is itself an old stem in that second
// generation. Applying the pairs in table order would rename a 617 row onto 637
// and then sweep it on to 657 — a database that ends up recording a migration
// whose SQL never ran, and one row short of the migration it did run.
func TestForkMigrationRenameOrderChainsGenerations(t *testing.T) {
	t.Parallel()

	rowsFor := func(from, to int) []string {
		rows := make([]string, 0, 20)
		for _, pair := range forkMigrationRenames {
			if n := migrationNumber(pair[0]); n >= from && n <= to {
				rows = append(rows, pair[0])
			}
		}
		return rows
	}

	cases := map[string][]string{
		"lane on v0.8.1 stems":           rowsFor(617, 640),
		"lane still on pre-v0.8.1 stems": rowsFor(595, 616),
	}

	for name, start := range cases {
		t.Run(name, func(t *testing.T) {
			if len(start) == 0 {
				t.Fatal("case built no rows")
			}
			rows := slices.Clone(start)
			// Same rule the SQL applies: move only when the target is absent.
			for _, pair := range forkMigrationRenameOrder() {
				oldVersion, newVersion := pair[0], pair[1]
				i := slices.Index(rows, oldVersion)
				if i < 0 || slices.Contains(rows, newVersion) {
					continue
				}
				rows[i] = newVersion
			}
			for _, stem := range rows {
				_, name, _ := strings.Cut(stem, "_")
				if want := finalStemOf(name); stem != want {
					t.Errorf("row ended on %q, want its newest stem %q", stem, want)
				}
			}
		})
	}
}
