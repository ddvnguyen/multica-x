package agent

import (
	"context"
	"fmt"
)

// BuiltinRuntime describes a built-in runtime identity that the daemon probes
// and registers automatically — independent of the custom runtime profile
// protocol_family whitelist. Multiple runtime identities can share one
// protocol family (e.g. both "pi" and "omp" use the "pi" protocol backend).
//
// The descriptor is the single declaration site for a runtime identity:
// agents_probe.go, config.go, daemon.go display-name overrides, execenv
// skill/config paths, local_skills.go, ListModels, and the frontend display
// maps all derive from this. Adding a new compatible fork of an existing
// runtime is a descriptor entry, not a cross-stack change.
type BuiltinRuntime struct {
	// ID is the provider key the daemon registers under (e.g. "omp").
	// It is NOT a protocol_family — it does not appear in SupportedTypes or
	// the runtime_profile.protocol_family CHECK constraint.
	ID string

	// ProtocolFamily is the execution backend this runtime dispatches to.
	// It MUST be in SupportedTypes. NewRuntime builds that family's backend
	// via New(), then applies this descriptor's ID-specific defaults
	// (executable, label) to it.
	ProtocolFamily string

	// DefaultCommand is the bare CLI name the probe looks up on PATH
	// when MULTICA_<ID>_PATH is not set (e.g. "omp").
	DefaultCommand string

	// EnvPrefix is the MULTICA_ prefix for *_PATH and *_MODEL env overrides
	// (e.g. "MULTICA_OMP" → MULTICA_OMP_PATH / MULTICA_OMP_MODEL).
	EnvPrefix string

	// DisplayName is the human-facing runtime name. The daemon and frontend
	// both use this so they never drift apart.
	DisplayName string

	// SkillsDir is the project-level skills directory relative to the
	// workdir (e.g. ".omp/skills").
	SkillsDir string

	// UserSkillsDir is the user-level skills directory relative to $HOME
	// (e.g. ".omp/agent/skills").
	UserSkillsDir string

	// LaunchHeader is the user-visible launch skeleton shown in the UI
	// (e.g. "omp (json mode)").
	LaunchHeader string

	// DefaultExecutable is the binary name the backend falls back to when
	// cfg.ExecutablePath is empty (passed to piBackend.defaultExecutable).
	DefaultExecutable string

	// ProviderLabel is the label used in log/error messages (passed to
	// piBackend.providerLabel).
	ProviderLabel string

	// ModelDiscovery is the strategy for discovering available models.
	// When set, it replaces the protocol family's discovery entirely — omp
	// uses `omp models --json`, a different command and output shape from
	// pi's `--list-models`. When nil, ListModels returns an empty catalog
	// rather than falling back to the family's command: running a
	// semantically incompatible one (omp exits non-zero on `--list-models`)
	// is worse than degrading to manual entry.
	ModelDiscovery ModelDiscoveryFunc
}

// ModelDiscoveryFunc discovers available models for a runtime identity.
// It receives the context and the resolved command — executable plus the
// runtime's launch prefix — and returns a model catalog. Discovery failures
// propagate to the picker, which shows the reason and supports manual entry.
type ModelDiscoveryFunc func(ctx context.Context, runtimeCmd Command) ([]Model, error)

// opencode2RuntimeID is the built-in runtime identity for the OpenCode 2 CLI.
// It is a constant because the descriptor table below references
// discoverOpenCode2Models, which must not reach back into BuiltinRuntimes
// during package initialization.
const opencode2RuntimeID = "opencode2"

// BuiltinRuntimes is the registry of built-in runtime identities that are
// NOT in SupportedTypes (they are protocol-family derivatives, not families
// themselves). The daemon probes each one independently and dispatches it
// to its protocol family's backend.
//
// A runtime identity appears here when it needs its own probe/display/skills
// but reuses an existing backend's protocol. The first entry is omp (oh-my-pi):
// a separate CLI that speaks the pi JSON event protocol.
var BuiltinRuntimes = []BuiltinRuntime{
	{
		ID:                "omp",
		ProtocolFamily:    "pi",
		DefaultCommand:    "omp",
		EnvPrefix:         "MULTICA_OMP",
		DisplayName:       "Oh-My-Pi",
		SkillsDir:         ".omp/skills",
		UserSkillsDir:     ".omp/agent/skills",
		LaunchHeader:      "omp (json mode)",
		DefaultExecutable: "omp",
		ProviderLabel:     "omp",
		ModelDiscovery:    discoverOmpModels,
	},
	{
		// OpenCode 2 is the 2.x CLI registered as its own runtime identity so a
		// host can run it beside the 1.x `opencode` binary. Everything below
		// the executable name — model discovery, `provider/model`
		// qualification, launch args, thinking variants — comes from the
		// opencode protocol family; the 2.x wire contract is detected from the
		// CLI's own version (see opencodeUsesV2Contract), not from this ID.
		ID:                opencode2RuntimeID,
		ProtocolFamily:    "opencode",
		DefaultCommand:    "opencode2",
		EnvPrefix:         "MULTICA_OPENCODE2",
		DisplayName:       "OpenCode 2",
		SkillsDir:         ".opencode/skills",
		UserSkillsDir:     ".config/opencode/skills",
		LaunchHeader:      "opencode2 run (json)",
		DefaultExecutable: "opencode2",
		ProviderLabel:     "opencode2",
		ModelDiscovery:    discoverOpenCode2Models,
	},
}

// BuiltinRuntimeByID returns the descriptor for the given runtime identity,
// or false if no such built-in runtime exists.
func BuiltinRuntimeByID(id string) (BuiltinRuntime, bool) {
	for _, r := range BuiltinRuntimes {
		if r.ID == id {
			return r, true
		}
	}
	return BuiltinRuntime{}, false
}

// IsBuiltinRuntime reports whether id is a registered built-in runtime
// identity (as opposed to a protocol family in SupportedTypes).
func IsBuiltinRuntime(id string) bool {
	_, ok := BuiltinRuntimeByID(id)
	return ok
}

// Family maps a runtime identity to the protocol family that executes it: a
// built-in runtime identity (e.g. "opencode2") resolves to its descriptor's
// family ("opencode"), and a protocol family resolves to itself.
//
// This is the one seam provider-keyed behaviour uses to accept a runtime
// identity. Anything that decides *how a runtime behaves* — thinking
// vocabulary, model qualification, blocked args, resume semantics — asks for
// the family; anything that reports *which runtime this is* (display name,
// metrics label, stored provider) keeps the identity.
func Family(provider string) string {
	if desc, ok := BuiltinRuntimeByID(provider); ok {
		return desc.ProtocolFamily
	}
	return provider
}

// BuiltinRuntimeCommands returns the default CLI command names for all
// built-in runtimes, for the daemon's defaultAgentCommandNames list.
func BuiltinRuntimeCommands() []string {
	cmds := make([]string, len(BuiltinRuntimes))
	for i, r := range BuiltinRuntimes {
		cmds[i] = r.DefaultCommand
	}
	return cmds
}

// backendOverrideApplicator is the interface that lets a descriptor apply
// per-runtime overrides (executable, label) to the backend its protocol family
// returned. A family whose backend does not implement it cannot host a runtime
// identity at all: NewRuntime fails closed rather than handing back a backend
// with the overrides silently dropped.
type backendOverrideApplicator interface {
	applyBuiltinRuntimeOverrides(desc BuiltinRuntime)
}

// piBackend already implements this via its defaultExecutable and
// providerLabel fields. The method is defined here (not in pi.go) to keep
// the descriptor→backend contract in one file.
func (b *piBackend) applyBuiltinRuntimeOverrides(desc BuiltinRuntime) {
	b.defaultExecutable = desc.DefaultExecutable
	b.providerLabel = desc.ProviderLabel
}

// ResolveBackend is the single production entry point the daemon uses to
// construct a backend from a provider key. It routes built-in runtime
// identities (e.g. "omp") through NewRuntime, and protocol families (e.g.
// "pi", "claude") through New. This keeps both factories meaning exactly one
// thing: New() is family-only, NewRuntime() is runtime-identity-only, and
// the daemon never has to know which is which.
func ResolveBackend(provider string, cfg Config) (Backend, error) {
	if IsBuiltinRuntime(provider) {
		return NewRuntime(provider, cfg)
	}
	return New(provider, cfg)
}

// NewRuntime creates a Backend for a built-in runtime identity (e.g. "omp").
// It builds the descriptor's protocol family backend via New(), then applies
// the per-runtime overrides through the backendOverrideApplicator interface.
// This is the production entry point for runtime identities; New() is
// family-only and rejects runtime ids.
//
// Fails closed: if the protocol family backend does not implement
// backendOverrideApplicator, the descriptor's overrides cannot be applied and
// the runtime would launch under the wrong executable and label — so
// NewRuntime returns an error rather than returning an unmodified backend.
func NewRuntime(runtimeID string, cfg Config) (Backend, error) {
	desc, ok := BuiltinRuntimeByID(runtimeID)
	if !ok {
		return nil, fmt.Errorf("unknown runtime identity: %q", runtimeID)
	}
	cfg.provider = runtimeID
	backend, err := New(desc.ProtocolFamily, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtime %q (family %q): %w", runtimeID, desc.ProtocolFamily, err)
	}
	applicator, ok := backend.(backendOverrideApplicator)
	if !ok {
		return nil, fmt.Errorf("runtime %q: protocol family %q backend %T does not support runtime overrides", runtimeID, desc.ProtocolFamily, backend)
	}
	applicator.applyBuiltinRuntimeOverrides(desc)
	return backend, nil
}

// ProfileRuntimeType preserves profiles authored before runtime identity was stored.
func ProfileRuntimeType(runtimeType, protocolFamily string) string {
	if runtimeType != "" {
		return runtimeType
	}
	return protocolFamily
}

// RuntimeProtocolFamily resolves the compatibility target using the same registry
// as backend construction and model discovery.
func RuntimeProtocolFamily(runtimeType string) (string, bool) {
	if desc, ok := BuiltinRuntimeByID(runtimeType); ok {
		return desc.ProtocolFamily, true
	}
	return runtimeType, IsSupportedType(runtimeType)
}
