package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
)

// compositionFixture builds an owner-in-workspace plus an implementing Agent
// and the Role it links to. Callers override owner/visibility on individual
// rows to exercise the fail-closed cases.
type compositionFixture struct {
	fx          *testutil.Fixture
	ownerID     string
	workspaceID string
	roleID      string
	agentID     string
	service     *TaskService
}

var compositionSeq atomic.Int64

func newCompositionFixture(t *testing.T, poolDB *pgxpool.Pool) compositionFixture {
	t.Helper()
	fx := testutil.New(poolDB, "", "")
	suffix := fmt.Sprintf("%d-%d", time.Now().UnixNano(), compositionSeq.Add(1))
	ownerID := fx.User(t, "composition owner", "composition-"+suffix+"@multica.test")
	workspaceID := fx.Workspace(t, "composition ws", "composition-"+suffix)
	fx.Member(t, workspaceID, ownerID, "owner")
	fx.UserID, fx.WorkspaceID = ownerID, workspaceID

	roleID := fx.Agent(t, "role-"+suffix, "", testutil.Cols{
		"subtype":   "role",
		"tier_role": "engineer",
	})
	agentID := fx.Agent(t, "agent-"+suffix, "", testutil.Cols{
		"subtype":            "agent",
		"implements_role_id": roleID,
	})
	return compositionFixture{
		fx:          fx,
		ownerID:     ownerID,
		workspaceID: workspaceID,
		roleID:      roleID,
		agentID:     agentID,
		service: &TaskService{
			Queries:      db.New(poolDB),
			FeatureFlags: roleFlagService(true, workspaceID),
		},
	}
}

func (f *compositionFixture) agentRow(t *testing.T, id string) db.Agent {
	t.Helper()
	row, err := f.service.Queries.GetAgent(context.Background(), util.MustParseUUID(id))
	if err != nil {
		t.Fatalf("load agent %s: %v", id, err)
	}
	return row
}

func (f *compositionFixture) seedSkill(t *testing.T, name, content string) string {
	t.Helper()
	return f.fx.Insert(t, "skill", testutil.Cols{
		"workspace_id": f.workspaceID,
		"name":         name,
		"description":  name + " description",
		"content":      content,
		"config":       testutil.Raw("'{}'::jsonb"),
		"created_by":   f.ownerID,
	})
}

func TestRoleComposition_InstructionsFor(t *testing.T) {
	role := RoleComposition{
		RoleID:           util.MustParseUUID("00000000-0000-0000-0000-000000000001"),
		RoleInstructions: "ROLE RULES",
	}
	if got := role.InstructionsFor("AGENT RULES"); got != "ROLE RULES\n\nAGENT RULES" {
		t.Fatalf("instruction order = %q, want role first", got)
	}
	if got := role.InstructionsFor(""); got != "ROLE RULES" {
		t.Fatalf("empty agent instructions = %q", got)
	}
	emptyRole := RoleComposition{RoleID: role.RoleID}
	if got := emptyRole.InstructionsFor("AGENT RULES"); got != "AGENT RULES" {
		t.Fatalf("empty role instructions = %q", got)
	}
	if got := (RoleComposition{}).InstructionsFor("AGENT RULES"); got != "AGENT RULES" {
		t.Fatalf("inactive composition = %q", got)
	}
}

func TestResolveRoleComposition_FailClosed(t *testing.T) {
	ctx := context.Background()
	pool := sharedTestPool(t)

	t.Run("happy path", func(t *testing.T) {
		f := newCompositionFixture(t, pool)
		f.fx.Exec(t, `UPDATE agent SET instructions = $1 WHERE id = $2`, "ROLE RULES", f.roleID)
		agent := f.agentRow(t, f.agentID)
		comp, err := f.service.ResolveRoleComposition(ctx, agent)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !comp.Active() || comp.RoleInstructions != "ROLE RULES" || util.UUIDToString(comp.RoleID) != f.roleID {
			t.Fatalf("composition = %+v", comp)
		}
		if len(comp.SkillAgentIDs) != 2 {
			t.Fatalf("skill agents = %v, want agent + role", comp.SkillAgentIDs)
		}
	})

	t.Run("deleted role", func(t *testing.T) {
		f := newCompositionFixture(t, pool)
		f.fx.Exec(t, `UPDATE agent SET implements_role_id = $1 WHERE id = $2`,
			"00000000-0000-0000-0000-000000009999", f.agentID)
		_, err := f.service.ResolveRoleComposition(ctx, f.agentRow(t, f.agentID))
		if !errors.Is(err, errRoleCompositionUnavailable) {
			t.Fatalf("deleted role err = %v, want fail closed", err)
		}
	})

	t.Run("archived role", func(t *testing.T) {
		f := newCompositionFixture(t, pool)
		f.fx.Exec(t, `UPDATE agent SET archived_at = now() WHERE id = $1`, f.roleID)
		_, err := f.service.ResolveRoleComposition(ctx, f.agentRow(t, f.agentID))
		if !errors.Is(err, errRoleCompositionUnavailable) {
			t.Fatalf("archived role err = %v, want fail closed", err)
		}
	})

	t.Run("cross workspace role", func(t *testing.T) {
		f := newCompositionFixture(t, pool)
		otherWS := f.fx.Workspace(t, "other ws", "other-ws-"+fmt.Sprint(compositionSeq.Add(1)))
		foreignRole := f.fx.Insert(t, "agent", testutil.Cols{
			"workspace_id": otherWS,
			"name":         "foreign-role",
			"runtime_mode": "cloud",
			"subtype":      "role",
			"tier_role":    "engineer",
			"owner_id":     f.ownerID,
		})
		f.fx.Exec(t, `UPDATE agent SET implements_role_id = $1 WHERE id = $2`, foreignRole, f.agentID)
		_, err := f.service.ResolveRoleComposition(ctx, f.agentRow(t, f.agentID))
		if !errors.Is(err, errRoleCompositionUnavailable) {
			t.Fatalf("cross workspace err = %v, want fail closed", err)
		}
	})

	t.Run("role inaccessible to agent owner", func(t *testing.T) {
		f := newCompositionFixture(t, pool)
		suffix := fmt.Sprintf("%d-%d", time.Now().UnixNano(), compositionSeq.Add(1))
		agentOwner := f.fx.User(t, "agent owner", "agent-owner-"+suffix+"@multica.test")
		roleOwner := f.fx.User(t, "role owner", "role-owner-"+suffix+"@multica.test")
		f.fx.Member(t, f.workspaceID, agentOwner, "member")
		f.fx.Member(t, f.workspaceID, roleOwner, "member")
		f.fx.Exec(t, `UPDATE agent SET owner_id = $1 WHERE id = $2`, roleOwner, f.roleID)
		f.fx.Exec(t, `UPDATE agent SET owner_id = $1 WHERE id = $2`, agentOwner, f.agentID)
		_, err := f.service.ResolveRoleComposition(ctx, f.agentRow(t, f.agentID))
		if !errors.Is(err, errRoleCompositionAccessDenied) {
			t.Fatalf("inaccessible role err = %v, want fail closed", err)
		}
	})

	t.Run("flag off and role run keep legacy path", func(t *testing.T) {
		f := newCompositionFixture(t, pool)
		f.service.FeatureFlags = roleFlagService(false, f.workspaceID)
		comp, err := f.service.ResolveRoleComposition(ctx, f.agentRow(t, f.agentID))
		if err != nil || comp.Active() {
			t.Fatalf("flag off: comp=%+v err=%v, want inactive", comp, err)
		}

		f.service.FeatureFlags = roleFlagService(true, f.workspaceID)
		roleRow := f.agentRow(t, f.roleID)
		comp, err = f.service.ResolveRoleComposition(ctx, roleRow)
		if err != nil || comp.Active() {
			t.Fatalf("role run: comp=%+v err=%v, want inactive", comp, err)
		}
	})
}

func TestLoadComposedSkills_UnionAndResolve(t *testing.T) {
	ctx := context.Background()
	f := newCompositionFixture(t, sharedTestPool(t))

	sharedID := f.seedSkill(t, "a-shared", "shared content")
	roleOnlyID := f.seedSkill(t, "b-role-only", "role content")
	agentOnlyID := f.seedSkill(t, "c-agent-only", "agent content")
	disabledID := f.seedSkill(t, "d-disabled", "disabled content")
	for _, skillID := range []string{sharedID, roleOnlyID} {
		f.fx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, f.roleID, skillID)
	}
	for _, skillID := range []string{sharedID, agentOnlyID} {
		f.fx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, f.agentID, skillID)
	}
	f.fx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id, enabled) VALUES ($1, $2, false)`, f.roleID, disabledID)

	agent := f.agentRow(t, f.agentID)
	comp, err := f.service.ResolveRoleComposition(ctx, agent)
	if err != nil {
		t.Fatalf("resolve composition: %v", err)
	}

	skills, err := f.service.LoadComposedSkills(ctx, comp, agent)
	if err != nil {
		t.Fatalf("load composed skills: %v", err)
	}
	var names []string
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	if got := strings.Join(names, ","); got != "a-shared,b-role-only,c-agent-only" {
		t.Fatalf("union = %q, want deduped name order", got)
	}

	_, refs, err := f.service.LoadComposedSkillBundles(ctx, comp, agent, "", false)
	if err != nil {
		t.Fatalf("load composed bundles: %v", err)
	}
	wanted := make([]AgentSkillBundleRef, 0, len(refs))
	for _, ref := range refs {
		// Unscoped built-ins are advertised to every agent; the composition
		// claim is about the workspace-skill union.
		if ref.Source != skillbundle.SourceWorkspace {
			continue
		}
		wanted = append(wanted, AgentSkillBundleRef{ID: ref.ID, Source: ref.Source})
	}
	if len(wanted) != 3 {
		t.Fatalf("workspace bundle refs = %d, want 3 (all refs = %d)", len(wanted), len(refs))
	}
	// A Role skill must resolve through the task Agent's claim refs.
	resolved, err := f.service.LoadRequestedComposedSkillBundles(ctx, comp, agent, wanted)
	if err != nil {
		t.Fatalf("resolve composed bundles: %v", err)
	}
	for _, skillID := range []string{sharedID, roleOnlyID, agentOnlyID} {
		found := false
		for _, bundle := range resolved {
			if bundle.ID == skillID {
				found = true
			}
		}
		if !found {
			t.Fatalf("skill %s missing from resolved bundles", skillID)
		}
	}
	if _, ok := resolved[AgentSkillBundleKey(skillbundle.SourceWorkspace, disabledID)]; ok {
		t.Fatal("disabled Role skill leaked into resolved bundles")
	}
	if len(resolved) != 3 {
		t.Fatalf("resolved bundles = %d, want 3 (one per deduped id)", len(resolved))
	}
}

func TestLoadComposedSkills_NoRoleAndRoleRunUnchanged(t *testing.T) {
	ctx := context.Background()
	f := newCompositionFixture(t, sharedTestPool(t))
	plainID := f.fx.Agent(t, "plain", "")
	skillID := f.seedSkill(t, "plain-skill", "plain content")
	f.fx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, plainID, skillID)

	for _, id := range []string{plainID, f.roleID} {
		row := f.agentRow(t, id)
		baseline, err := f.service.LoadAgentSkills(ctx, row.ID)
		if err != nil {
			t.Fatalf("baseline load: %v", err)
		}
		composed, err := f.service.LoadComposedSkills(ctx, RoleComposition{}, row)
		if err != nil {
			t.Fatalf("composed load: %v", err)
		}
		if len(baseline) != len(composed) {
			t.Fatalf("agent %s: baseline=%d composed=%d", id, len(baseline), len(composed))
		}
		for i := range baseline {
			if baseline[i].ID != composed[i].ID || baseline[i].Content != composed[i].Content {
				t.Fatalf("agent %s skill %d changed: %+v vs %+v", id, i, baseline[i], composed[i])
			}
		}
	}
}
