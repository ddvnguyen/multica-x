package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// M-423: planning writes difficulty. The two definition names the resolver
// reads; created on demand by ensureDifficultyPropertiesTx.
var difficultyPropertyNames = []string{"important_level", "complexity"}

// ensureDifficultyPropertiesTx runs inside the plan-approve transaction: it
// ensures active number-typed definitions for important_level/complexity in
// the workspace (creating them when absent), then defaults the issue's
// missing or invalid values to 1. Human-set 2..5 values are never touched.
// Definition choice is deterministic: active, workspace-scoped, number
// type, case-insensitive name match; on duplicates the lowest property-UUID
// (lexicographic) wins — the same rule difficultyFromProperties applies at
// resolve time, so writer and reader agree even with renames/duplicates.
func ensureDifficultyPropertiesTx(ctx context.Context, qtx *db.Queries, workspaceID, issueID pgtype.UUID) error {
	defs, err := qtx.ListIssueProperties(ctx, db.ListIssuePropertiesParams{
		WorkspaceID:     workspaceID,
		IncludeArchived: false,
	})
	if err != nil {
		return err
	}
	byName := map[string]db.ListIssuePropertiesRow{}
	for _, d := range defs {
		if d.ArchivedAt.Valid || d.Type != "number" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(d.Name))
		if key != "important_level" && key != "complexity" {
			continue
		}
		if prev, ok := byName[key]; !ok || util.UUIDToString(d.ID) < util.UUIDToString(prev.ID) {
			byName[key] = d
		}
	}
	issue, err := qtx.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}
	var bag map[string]json.RawMessage
	if len(issue.Properties) > 0 {
		if err := json.Unmarshal(issue.Properties, &bag); err != nil {
			bag = map[string]json.RawMessage{}
		}
	}
	if bag == nil {
		bag = map[string]json.RawMessage{}
	}
	for _, name := range difficultyPropertyNames {
		def, ok := byName[name]
		if !ok {
			made, err := qtx.CreateIssueProperty(ctx, db.CreateIssuePropertyParams{
				WorkspaceID: workspaceID,
				Name:        name,
				Type:        "number",
				Description: "M-423 difficulty (1-5); set during planning, raises the tier quality floor.",
			})
			if err != nil {
				return err
			}
			def = db.ListIssuePropertiesRow{
				ID:          made.ID,
				WorkspaceID: made.WorkspaceID,
				Name:        made.Name,
				Type:        made.Type,
			}
			byName[name] = def
		}
		key := util.UUIDToString(def.ID)
		if tierPropLevel(bag[key]) != 1 || !isValidDifficultyValue(bag[key]) {
			if _, err := qtx.SetIssuePropertyValue(ctx, db.SetIssuePropertyValueParams{
				Key:         key,
				Value:       []byte("1"),
				ID:          issueID,
				WorkspaceID: workspaceID,
			}); err != nil {
				return err
			}
			bag[key] = json.RawMessage("1")
		}
	}
	return nil
}

// isValidDifficultyValue reports whether the stored JSON is an integral 1..5.
func isValidDifficultyValue(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return false
	}
	return f == float64(int(f)) && f >= 1 && f <= 5
}
