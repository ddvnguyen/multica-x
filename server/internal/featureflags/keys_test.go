package featureflags

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// The Role subtype is a workspace-targetable, default-off flag: the helper
// must attach the workspace to the evaluation context so an allow_by rule can
// enable it on lane z alone, and fail closed when no workspace is known.
func TestAgentRoleTypeEnabledForWorkspace(t *testing.T) {
	provider := featureflag.NewStaticProvider()
	provider.Set(AgentRoleType, featureflag.Rule{
		Default: false,
		AllowBy: "workspace_id",
		Allow:   []string{"lane-z-workspace"},
	})
	flags := featureflag.NewService(provider)
	ctx := context.Background()

	if !AgentRoleTypeEnabledForWorkspace(ctx, flags, "lane-z-workspace") {
		t.Fatal("targeted workspace must see the Role flag enabled")
	}
	if AgentRoleTypeEnabledForWorkspace(ctx, flags, "other-workspace") {
		t.Fatal("untargeted workspace must see the Role flag disabled")
	}
	if AgentRoleTypeEnabledForWorkspace(ctx, flags, "") {
		t.Fatal("empty workspace id must fail closed")
	}
	if AgentRoleTypeEnabledForWorkspace(ctx, nil, "lane-z-workspace") {
		t.Fatal("nil flag service must fail closed")
	}
}
