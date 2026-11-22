package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// roleFlagService returns the workspace-targeted flag service the Role
// exemption reads through RuntimeLookup. The default-off form is the
// production shape; a Role must fail closed without an explicit allow rule.
func roleFlagService(enabled bool, workspaceID string) *featureflag.Service {
	provider := featureflag.NewStaticProvider()
	rule := featureflag.Rule{Default: false}
	if enabled {
		rule = featureflag.Rule{Default: false, AllowBy: "workspace_id", Allow: []string{workspaceID}}
	}
	provider.Set(featureflags.AgentRoleType, rule)
	return featureflag.NewService(provider)
}

// TestAgentReadiness_RoleRuntimeless is the single admission predicate used by
// assign, @mention, chat, quick-create and autopilot: a flag-on Role has no
// runtime by design and must pass, while everything else still fails closed.
func TestAgentReadiness_RoleRuntimeless(t *testing.T) {
	ctx := context.Background()
	workspaceID := "00000000-0000-0000-0000-000000000510"
	workspaceUUID := util.MustParseUUID(workspaceID)
	role := db.Agent{
		ID:             util.MustParseUUID("00000000-0000-0000-0000-000000000511"),
		WorkspaceID:    workspaceUUID,
		Subtype:        "role",
		TierRole:       pgtype.Text{String: "engineer", Valid: true},
		PermissionMode: "private",
	}
	plain := role
	plain.Subtype = "agent"
	archived := role
	archived.ArchivedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}

	// Flag on: the Role is available — the tier resolver supplies its route.
	verdict, err := AgentReadiness(ctx, RuntimeLookup{FeatureFlags: roleFlagService(true, workspaceID)}, role)
	if err != nil {
		t.Fatalf("role readiness: %v", err)
	}
	if !verdict.Ready() || verdict.Blocked() || verdict.Reason != "" {
		t.Fatalf("flag-on Role verdict = %+v, want available", verdict)
	}

	// Flag off, nil flags, a plain runtime-less Agent and an archived Role
	// remain blocked with the original reason codes.
	for _, tc := range []struct {
		name   string
		agent  db.Agent
		flags  *featureflag.Service
		reason dispatch.ReasonCode
	}{
		{"flag-off role", role, roleFlagService(false, workspaceID), dispatch.ReasonAgentRuntimeRequired},
		{"nil flags role", role, nil, dispatch.ReasonAgentRuntimeRequired},
		{"plain agent", plain, roleFlagService(true, workspaceID), dispatch.ReasonAgentRuntimeRequired},
		{"archived role", archived, roleFlagService(true, workspaceID), dispatch.ReasonTargetUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AgentReadiness(ctx, RuntimeLookup{FeatureFlags: tc.flags}, tc.agent)
			if err != nil {
				t.Fatalf("readiness: %v", err)
			}
			if !got.Blocked() || got.Reason != tc.reason {
				t.Fatalf("verdict = %+v, want blocked/%s", got, tc.reason)
			}
		})
	}
}

// TestAgentAssigneeVerdict_Role exercises the assign-side caller with a real
// row: it must hand the Role to the enqueue path when the flag is on and refuse
// it when the flag is off, without any per-call-site Role logic.
func TestAgentAssigneeVerdict_Role(t *testing.T) {
	pool := sharedTestPool(t)
	ctx := context.Background()
	fx := testutil.New(pool, "", "")
	fx.UserID = fx.User(t, "role readiness", fmt.Sprintf("role-readiness-%d@multica.test", time.Now().UnixNano()))
	fx.WorkspaceID = fx.Workspace(t, "role readiness ws", fmt.Sprintf("role-readiness-ws-%d", time.Now().UnixNano()))
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	roleID := fx.Agent(t, "role-readiness-role", "", testutil.Cols{"subtype": "role", "tier_role": "engineer"})
	issueID := fx.Issue(t, "role readiness issue", testutil.Cols{
		"assignee_type": "agent", "assignee_id": roleID,
	})
	issue, err := db.New(pool).GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	lookup := RuntimeLookup{Queries: db.New(pool)}

	lookup.FeatureFlags = roleFlagService(true, fx.WorkspaceID)
	if verdict, ok := agentAssigneeVerdict(ctx, lookup, issue); !ok || !verdict.Ready() {
		t.Fatalf("flag-on Role assign verdict = %+v ok=%v, want ready", verdict, ok)
	}
	lookup.FeatureFlags = roleFlagService(false, fx.WorkspaceID)
	if verdict, ok := agentAssigneeVerdict(ctx, lookup, issue); ok || verdict.Reason != dispatch.ReasonAgentRuntimeRequired {
		t.Fatalf("flag-off Role assign verdict = %+v ok=%v, want blocked", verdict, ok)
	}
}
