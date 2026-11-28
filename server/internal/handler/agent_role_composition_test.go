package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var compositionHandlerSeq int64

func compositionHandlerSuffix() string {
	compositionHandlerSeq++
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), compositionHandlerSeq)
}

// seedRoleCompositionClaim builds a Role and an implementing Agent with an
// overlapping skill set: one shared, one Role-only, one Agent-only. It returns
// the runtime, Agent, Role, issue and the three workspace skill ids.
func seedRoleCompositionClaim(t *testing.T, ctx context.Context, name string) (runtimeID, agentID, roleID, issueID string, sharedID, roleOnlyID, agentOnlyID string) {
	t.Helper()
	runtimeID = createClaimReclaimRuntime(t, ctx, name+" runtime")
	roleID = dbfx.Agent(t, name+" role", "", testutil.Cols{
		"subtype":      "role",
		"tier_role":    "engineer",
		"instructions": "ROLE RULES",
	})
	agentID, issueID = createClaimReclaimAgentAndIssue(t, ctx, runtimeID, name+" agent")
	dbfx.Exec(t, `UPDATE agent SET subtype = 'agent', instructions = $2, implements_role_id = $3 WHERE id = $1`,
		agentID, "AGENT RULES", roleID)

	newSkill := func(label, content string) string {
		return dbfx.Insert(t, "skill", testutil.Cols{
			"workspace_id": testWorkspaceID,
			"name":         name + "-" + label,
			"description":  label + " description",
			"content":      content,
			"config":       testutil.Raw("'{}'::jsonb"),
			"created_by":   testUserID,
		})
	}
	sharedID = newSkill("shared", "shared content")
	roleOnlyID = newSkill("role-only", "role content")
	agentOnlyID = newSkill("agent-only", "agent content")
	dbfx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, roleID, sharedID)
	dbfx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, roleID, roleOnlyID)
	dbfx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, agentID, sharedID)
	dbfx.Exec(t, `INSERT INTO agent_skill (agent_id, skill_id) VALUES ($1, $2)`, agentID, agentOnlyID)
	return
}

func roleCompositionClaim(t *testing.T, runtimeID, capability string) AgentTaskResponse {
	t.Helper()
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil)
	if capability != "" {
		req.Header.Set("X-Client-Capabilities", capability)
	}
	req = withURLParam(req, "runtimeId", runtimeID)
	w := testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK)
	var resp struct {
		Task *AgentTaskResponse `json:"task"`
	}
	w.JSON(&resp)
	if resp.Task == nil || resp.Task.Agent == nil {
		t.Fatalf("claim has no task agent: %s", w.Body.String())
	}
	return *resp.Task
}

func workspaceSkillNames(refs []service.AgentSkillRefData, full []service.AgentSkillData) map[string]bool {
	names := make(map[string]bool)
	for _, ref := range refs {
		if ref.Source == "workspace" {
			names[ref.Name] = true
		}
	}
	for _, skill := range full {
		// Built-ins are platform-wide, not part of the Role/Agent union.
		if skill.ID == "" || strings.HasPrefix(skill.ID, "builtin:") || skill.Source == "builtin" {
			continue
		}
		names[skill.Name] = true
	}
	return names
}

// TestClaimTaskByRuntime_RoleComposition covers the two claim payload paths:
// slim skill refs and legacy inline skills. Both must carry Role instructions
// first, Agent additions second, and the deduplicated union of enabled skills.
func TestClaimTaskByRuntime_RoleComposition(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	ctx := context.Background()
	name := "role-composition-" + compositionHandlerSuffix()
	runtimeID, agentID, _, issueID, sharedID, roleOnlyID, agentOnlyID := seedRoleCompositionClaim(t, ctx, name)

	wanted := map[string]bool{}
	for _, id := range []string{sharedID, roleOnlyID, agentOnlyID} {
		var skillName string
		if err := testPool.QueryRow(ctx, `SELECT name FROM skill WHERE id = $1`, id).Scan(&skillName); err != nil {
			t.Fatalf("load skill name: %v", err)
		}
		wanted[skillName] = true
	}

	// Slim claim: Role skills must appear as refs the daemon can resolve.
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})
	slim := roleCompositionClaim(t, runtimeID, protocol.DaemonCapabilitySkillBundlesV1)
	if got := slim.Agent.Instructions; got != "ROLE RULES\n\nAGENT RULES" {
		t.Fatalf("claim instructions = %q, want Role first then Agent", got)
	}
	if len(slim.Agent.SkillRefs) == 0 {
		t.Fatal("slim claim has no skill refs")
	}
	if got := workspaceSkillNames(slim.Agent.SkillRefs, nil); len(got) != len(wanted) {
		t.Fatalf("slim skill names = %v, want %v", got, wanted)
	}
	for skillName := range wanted {
		if !workspaceSkillNames(slim.Agent.SkillRefs, nil)[skillName] {
			t.Fatalf("slim claim missing skill %q", skillName)
		}
	}

	// The Role-only ref must resolve through the task Agent's junction.
	var roleRef service.AgentSkillRefData
	for _, ref := range slim.Agent.SkillRefs {
		if ref.ID == roleOnlyID {
			roleRef = ref
		}
	}
	if roleRef.ID == "" || roleRef.Hash == "" {
		t.Fatalf("Role-only skill ref missing: %+v", slim.Agent.SkillRefs)
	}
	taskID := slim.ID
	resolveBody := resolveSkillBundlesRequest{Skills: []resolveSkillBundleRef{{
		ID: roleRef.ID, Source: roleRef.Source, Hash: roleRef.Hash,
	}}}
	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/"+taskID+"/skill-bundles/resolve", resolveBody)
	req = withURLParams(req, "runtimeId", runtimeID, "taskId", taskID)
	w := testutil.Call(t, testHandler.ResolveTaskSkillBundles, req).Want(http.StatusOK)
	var resolved struct {
		Bundles []service.AgentSkillData `json:"bundles"`
	}
	w.JSON(&resolved)
	if len(resolved.Bundles) != 1 || resolved.Bundles[0].Content != "role content" {
		t.Fatalf("Role skill bundle did not resolve: %+v", resolved.Bundles)
	}

	// Legacy path: full inline skills, same deduplicated union. A fresh runtime
	// keeps this claim independent of the first task's in-flight state.
	legacyRuntimeID, legacyAgentID, _, legacyIssueID, lShared, lRoleOnly, lAgentOnly := seedRoleCompositionClaim(t, ctx, name+"-legacy")
	dbfx.Task(t, legacyAgentID, testutil.Cols{"runtime_id": legacyRuntimeID, "issue_id": legacyIssueID})
	legacy := roleCompositionClaim(t, legacyRuntimeID, "")
	if got := legacy.Agent.Instructions; got != "ROLE RULES\n\nAGENT RULES" {
		t.Fatalf("legacy claim instructions = %q", got)
	}
	legacyWanted := map[string]bool{}
	for _, id := range []string{lShared, lRoleOnly, lAgentOnly} {
		var skillName string
		if err := testPool.QueryRow(ctx, `SELECT name FROM skill WHERE id = $1`, id).Scan(&skillName); err != nil {
			t.Fatalf("load legacy skill name: %v", err)
		}
		legacyWanted[skillName] = true
	}
	if got := workspaceSkillNames(nil, legacy.Agent.Skills); len(got) != len(legacyWanted) {
		t.Fatalf("legacy skill names = %v, want %v", got, legacyWanted)
	}
	for skillName := range legacyWanted {
		if !workspaceSkillNames(nil, legacy.Agent.Skills)[skillName] {
			t.Fatalf("legacy claim missing skill %q", skillName)
		}
	}
}

// TestClaimTaskByRuntime_RoleUnavailableFailsClosed proves a deleted Role
// never turns into a Role-less claim payload.
func TestClaimTaskByRuntime_RoleUnavailableFailsClosed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	ctx := context.Background()
	name := "role-gone-" + compositionHandlerSuffix()
	runtimeID, agentID, _, issueID, _, _, _ := seedRoleCompositionClaim(t, ctx, name)
	dbfx.Exec(t, `UPDATE agent SET implements_role_id = $1 WHERE id = $2`,
		"00000000-0000-0000-0000-000000009999", agentID)
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})

	req := newOwnerDaemonRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil)
	req = withURLParam(req, "runtimeId", runtimeID)
	w := testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK)
	// failClaimedTaskBeforeLaunch settles the task and returns "no work" to
	// the daemon: there must be no Role-less payload in the response.
	var resp struct {
		Task *AgentTaskResponse `json:"task"`
	}
	w.JSON(&resp)
	if resp.Task != nil {
		t.Fatalf("deleted Role still produced a claim payload: %+v", resp.Task)
	}
	var status string
	var errorText string
	dbfx.QueryRow(t, `SELECT status, COALESCE(error, '') FROM agent_task_queue WHERE agent_id = $1`, agentID).Scan(&status, &errorText)
	if status == "dispatched" || !strings.Contains(errorText, "Role") {
		t.Fatalf("task status=%q error=%q, want settled with a clear Role error", status, errorText)
	}
}

// TestPreviewRoleComposition covers the read-only preview API and, crucially,
// that a member who can see the Agent but not its private Role gets no Role
// content at all.
func TestPreviewRoleComposition(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	ctx := context.Background()
	name := "role-preview-" + compositionHandlerSuffix()
	runtimeID, agentID, roleID, _, sharedID, roleOnlyID, agentOnlyID := seedRoleCompositionClaim(t, ctx, name)
	_ = runtimeID

	previewRequest := func(userID, agentID string) *http.Request {
		req := newRequest(http.MethodGet, "/api/agents/"+agentID+"/role-preview", nil)
		req.Header.Set("X-User-ID", userID)
		return withURLParam(req, "id", agentID)
	}

	// Workspace owner sees both: full composition with per-skill source.
	w := testutil.Call(t, testHandler.PreviewRoleComposition, previewRequest(testUserID, agentID)).Want(http.StatusOK)
	var preview RoleCompositionPreview
	w.JSON(&preview)
	if preview.Instructions != "ROLE RULES\n\nAGENT RULES" || preview.RoleID == nil || *preview.RoleID != roleID {
		t.Fatalf("preview = %+v", preview)
	}
	sources := map[string]string{}
	for _, skill := range preview.Skills {
		sources[skill.ID] = skill.Source
	}
	if sources[sharedID] != "role" || sources[roleOnlyID] != "role" || sources[agentOnlyID] != "agent" || len(sources) != 3 {
		t.Fatalf("preview skill sources = %v", sources)
	}

	// A regular member allowed to invoke the Agent but not to read the Role
	// must get 403 and no inherited content.
	suffix := compositionHandlerSuffix()
	viewerID := dbfx.Insert(t, "user", testutil.Cols{"name": "role viewer", "email": "role-viewer-" + suffix + "@multica.test"})
	dbfx.Insert(t, "member", testutil.Cols{"workspace_id": testWorkspaceID, "user_id": viewerID, "role": "member"})
	dbfx.Exec(t, `UPDATE agent SET permission_mode = 'public_to' WHERE id = $1`, agentID)
	dbfx.Exec(t, `INSERT INTO agent_invocation_target (agent_id, target_type, target_id) VALUES ($1, 'member', $2)`, agentID, viewerID)
	w = testutil.Call(t, testHandler.PreviewRoleComposition, previewRequest(viewerID, agentID)).Want(http.StatusForbidden)
	if strings.Contains(w.Body.String(), "ROLE RULES") {
		t.Fatalf("preview leaked Role content: %s", w.Body.String())
	}

	// Flag off keeps the whole surface dark.
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, false)
	testutil.Call(t, testHandler.PreviewRoleComposition, previewRequest(testUserID, agentID)).Want(http.StatusForbidden)
}

// TestClaimTaskByRuntime_MoctReadExpertID pins the M-542 claim wiring: a run
// whose Agent implements a Role carries the Role scope, while a Role-less run
// and a direct Role run leave the field absent (byte-identical seam).
func TestClaimTaskByRuntime_MoctReadExpertID(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	ctx := context.Background()
	name := "moct-read-expert-" + compositionHandlerSuffix()
	runtimeID, agentID, roleID, issueID, _, _, _ := seedRoleCompositionClaim(t, ctx, name)
	seedQueuedIssueTask(t, ctx, agentID, runtimeID, issueID)

	resp := roleCompositionClaim(t, runtimeID, "")
	if resp.MoctReadExpertID == nil {
		t.Fatal("implementing-Agent claim carries no moct_read_expert_id")
	}
	if want := "multica:agent:" + roleID; *resp.MoctReadExpertID != want {
		t.Fatalf("moct_read_expert_id = %q, want %q", *resp.MoctReadExpertID, want)
	}

	// A Role-less Agent on its own runtime: the field stays absent.
	plainRuntimeID := createClaimReclaimRuntime(t, ctx, name+" plain runtime")
	plainAgentID, plainIssueID := createClaimReclaimAgentAndIssue(t, ctx, plainRuntimeID, name+" plain agent")
	seedQueuedIssueTask(t, ctx, plainAgentID, plainRuntimeID, plainIssueID)
	plain := roleCompositionClaim(t, plainRuntimeID, "")
	if plain.MoctReadExpertID != nil {
		t.Fatalf("role-less claim carries moct_read_expert_id = %q, want absent", *plain.MoctReadExpertID)
	}
}

// TestClaimTaskByRuntime_MikaKeepsRoleInstructions: the Mika system-agent
// override must layer over the Role-composed instructions (M-516), so a Mika
// that implements a Role carries Mika's prompt, the Role text and its own notes.
func TestClaimTaskByRuntime_MikaKeepsRoleInstructions(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.AgentRoleType, true)
	ctx := context.Background()
	name := "role-mika-" + compositionHandlerSuffix()
	runtimeID, agentID, _, issueID, _, _, _ := seedRoleCompositionClaim(t, ctx, name)
	dbfx.Exec(t, `UPDATE agent SET system_key = $2 WHERE id = $1`, agentID, service.MikaSystemKey)
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "issue_id": issueID})

	got := roleCompositionClaim(t, runtimeID, protocol.DaemonCapabilitySkillBundlesV1).Agent.Instructions
	for _, want := range []string{"Multica's built-in system agent (Mika)", "ROLE RULES", "AGENT RULES"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Mika claim instructions missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "ROLE RULES") > strings.Index(got, "AGENT RULES") {
		t.Fatalf("Role text must precede Agent additions:\n%s", got)
	}
}
