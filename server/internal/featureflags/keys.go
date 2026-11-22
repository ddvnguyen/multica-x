package featureflags

import (
	"context"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

const (
	// BillingWorkspaceSubscriptions gates the workspace-scoped entitlement,
	// Stripe Checkout, seat reconcile, and Billing Portal proxy surface. It is
	// deliberately off by default so the main repository can ship before the
	// managed cloud enables its matching billing.subscriptions capability.
	BillingWorkspaceSubscriptions = "billing_workspace_subscriptions"
	// ComposioMCPApps gates the Composio app management UI and — together with
	// the MUL-3963 permission_mode / invocation_targets access model it depends
	// on — the aligned Private / Public-to picker in the agent create flow.
	// The access model exists to gate Composio sharing, so the two ship on the
	// same switch.
	ComposioMCPApps = "composio_mcp_apps"
	// PluginsV1 gates the user-facing Plugin catalog and lifecycle management
	// APIs while the first product slice is dogfooded. It deliberately does not
	// gate pinned Task/Run execution: disabling discovery and management must not
	// mutate an immutable execution manifest that is already in flight.
	PluginsV1 = "plugins_v1"
	// TriageV1 gates the Triage inbox (MUL-7189): creating issues into Triage,
	// triage runs, and the Triage page. It is a GLOBAL switch — per-workspace
	// targeting has no production wiring — which is enough because a
	// workspace with no triager configured and no Triage issues sees nothing
	// either way. Turning it off stops new intake and triage runs but leaves
	// existing Triage issues workable.
	TriageV1 = "triage_v1"
	// LocalSearchIndex gates the sync endpoints behind Web/Desktop local search
	// (MUL-7754). On by default. Turning it off (FF_LOCAL_SEARCH_INDEX=off)
	// makes those endpoints answer 404, so clients stop syncing and search
	// through the server again without a client release.
	LocalSearchIndex = "local_search_index"
	// DocsV1 gates workspace Docs (MUL-8370) while it is still being tested:
	// the /api/docs and /api/doc-folders APIs, the Web/Desktop entry points,
	// and the doc lines in the agent brief. Off by default so a release can
	// ship the code without launching the feature; FF_DOCS_V1=true turns it on
	// for a deployment. It is a GLOBAL switch. Turning it off leaves existing
	// doc rows untouched — they simply become unreachable until it is back on.
	DocsV1 = "docs_v1"
	// agentBuilderCompat is no longer a release flag. Keep publishing the key
	// as enabled so installed desktop clients that still gate the AI creation
	// entry on this config decision receive the permanently enabled behavior.
	agentBuilderCompat = "agents_agent_builder"
	// agentSkillTogglesCompat is no longer a release flag. Keep publishing the
	// key as enabled so installed v0.4.0 desktop clients, which still gate the
	// switch on this config decision, receive the permanently enabled behavior.
	agentSkillTogglesCompat = "agents_skill_toggles"
	// resourceLabelsCompat is no longer a release flag. Keep publishing the key
	// as enabled for installed desktop clients from v0.4.0 through at least
	// v0.4.15, every release shipped before this change. Unlike the skill-toggle
	// gate above, which was removed client-side in v0.4.1, the resource-label
	// gate remained in every such client and fails closed (default false) if
	// the key stops being published.
	resourceLabelsCompat = "settings_resource_labels"
	// projectWorkflowsCompat keeps workflow UI available in installed desktop
	// clients that still gate it on this config decision and default to false.
	projectWorkflowsCompat = "project_workflows_v1"
	// AgentRoleType gates the additive agent subtype (`agent` | `role`) and the
	// implements_role_id / tier_role fields on the agent API (M-499 slice 1).
	// Off by default: enable it for a single workspace with an allow rule keyed
	// on workspace_id. Existing Agents and their direct/use_tiers flows never
	// read this flag.
	AgentRoleType = "agent_role_type"
)

var frontendPublicFlags = []string{
	BillingWorkspaceSubscriptions,
	ComposioMCPApps,
	PluginsV1,
	DocsV1,
	// Published so Team/Role UI can gate on it; lane-only until the
	// FF_AGENT_ROLE_TYPE env override is set elsewhere. Off by default.
	AgentRoleType,
}

func BillingWorkspaceSubscriptionsEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, BillingWorkspaceSubscriptions, false)
}

func ComposioMCPAppsEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, ComposioMCPApps, false)
}

func PluginsV1Enabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, PluginsV1, false)
}

func TriageV1Enabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, TriageV1, false)
}

func LocalSearchIndexEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, LocalSearchIndex, true)
}

func DocsV1Enabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, DocsV1, false)
}

// AgentRoleTypeEnabledForWorkspace evaluates AgentRoleType with the workspace
// attached to the flag context, so `allow_by: workspace_id` rules can enable
// the Role subtype for one workspace (lane z) while the default stays off.
// An empty workspace id fails closed.
func AgentRoleTypeEnabledForWorkspace(ctx context.Context, flags *featureflag.Service, workspaceID string) bool {
	if workspaceID == "" || flags == nil {
		return false
	}
	scoped := featureflag.WithEvalContext(ctx, featureflag.EvalContext{WorkspaceID: workspaceID})
	return flags.IsEnabled(scoped, AgentRoleType, false)
}

func EvaluateFrontendPublicFlags(ctx context.Context, flags *featureflag.Service) map[string]bool {
	out := make(map[string]bool, len(frontendPublicFlags)+4)
	for _, key := range frontendPublicFlags {
		out[key] = flags.IsEnabled(ctx, key, false)
	}
	out[agentBuilderCompat] = true
	out[agentSkillTogglesCompat] = true
	out[resourceLabelsCompat] = true
	out[projectWorkflowsCompat] = true
	return out
}
