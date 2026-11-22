package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// tierNamePattern keeps names slug-safe: M-359 addresses tiers as
// @agent#tier-name, so the name must survive verbatim in a mention.
var tierNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateTierName enforces the slug-safe shape and length.
func validateTierName(n string) error {
	if n == "" {
		return errors.New("name is required")
	}
	if len(n) > 40 || !tierNamePattern.MatchString(n) {
		return errors.New("name must be 1-40 lowercase letters, digits, or hyphens, starting with a letter or digit")
	}
	return nil
}

// isTierNameConflict reports the unique (workspace_id, lower(name)) violation.
func isTierNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Workspace model tiers (M-350).
//
// A tier names one runtime/provider/model combo with an order index (0 first,
// 99 last), display cost/quality, and provider_slots — the max simultaneous
// sessions on that runtime/provider (e.g. Freebuff = 1). It is NOT the agent's
// max_concurrent_tasks. Resolution at enqueue and the agent-page switch are
// M-351; this card is the table, the admin CRUD, and the Settings UI backing.
//
// Reads are member-visible (agent owners will pick from these in M-351).
// Writes are owner/admin; the router gates them and each handler re-checks.

// ModelTierResponse is the wire shape of one tier profile (M-481).
type ModelTierResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	Name          string  `json:"name"`
	Idx           int     `json:"idx"`
	RuntimeID     string  `json:"runtime_id,omitempty"`
	Provider      string  `json:"provider"`
	Model         string  `json:"model"`
	ThinkingLevel string  `json:"thinking_level,omitempty"`
	ServiceTier   string  `json:"service_tier,omitempty"`
	Cost          *string `json:"cost,omitempty"`
	Quality       *int    `json:"quality,omitempty"`
	ProviderSlots int     `json:"provider_slots"`
	Enabled       bool    `json:"enabled"`
	// Roles served by this tier (M-423). Empty = legacy/unconfigured: matches
	// only agents with no role set. M-497: derived from model_tier_role rows.
	Roles []string `json:"roles"`
	// RoleIdx is this tier's position inside the requested role's list
	// (M-497). Present only when the list was filtered with ?role=.
	RoleIdx *int `json:"role_idx,omitempty"`
	// ManualOnly is legacy compat (M-481: auto_pick = NOT manual_only).
	ManualOnly bool `json:"manual_only"`
	// AutoPick: when true the system may pick this tier by idx/quota/quality.
	// When false, manual only (@agent#tier or picker).
	AutoPick bool `json:"auto_pick"`
	// QuotaKind is legacy scalar compat; QuotaKinds is the multi-select form.
	QuotaKind  string   `json:"quota_kind"`
	QuotaKinds []string `json:"quota_kinds"`
	IsFree     bool     `json:"is_free"`
	// Context and compaction profile (stored/shown; harness enforcement may be
	// advisory until proven per harness).
	CtxMaxTokens int `json:"ctx_max_tokens"`
	CompactAtPct int `json:"compact_at_pct"`
	// CooldownUntil is when a provider-refusal cooldown expires (M-352). Omitted
	// when the tier is not cooling; the resolver skips a tier while it is set
	// and in the future, and the Settings row renders it as "until HH:MM".
	CooldownUntil string `json:"cooldown_until,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func modelTierToResponse(t db.ModelTier) ModelTierResponse {
	resp := ModelTierResponse{
		ID:            uuidToString(t.ID),
		WorkspaceID:   uuidToString(t.WorkspaceID),
		Name:          t.Name,
		Idx:           int(t.Idx),
		Provider:      t.Provider,
		Model:         t.Model,
		ProviderSlots: int(t.ProviderSlots),
		Enabled:       t.Enabled,
		Roles:         append([]string{}, t.Roles...),
		ManualOnly:    t.ManualOnly,
		AutoPick:      t.AutoPick,
		QuotaKind:     t.QuotaKind,
		QuotaKinds:    append([]string{}, t.QuotaKinds...),
		IsFree:        t.IsFree,
		CtxMaxTokens:  int(t.CtxMaxTokens),
		CompactAtPct:  int(t.CompactAtPct),
		CreatedAt:     timestampToString(t.CreatedAt),
		UpdatedAt:     timestampToString(t.UpdatedAt),
	}
	if resp.QuotaKinds == nil {
		resp.QuotaKinds = []string{}
	}
	if resp.CtxMaxTokens == 0 {
		resp.CtxMaxTokens = 280000
	}
	if resp.CompactAtPct == 0 {
		resp.CompactAtPct = 75
	}
	if t.RuntimeID.Valid {
		resp.RuntimeID = uuidToString(t.RuntimeID)
	}
	if t.ThinkingLevel.Valid {
		resp.ThinkingLevel = t.ThinkingLevel.String
	}
	if t.ServiceTier.Valid {
		resp.ServiceTier = t.ServiceTier.String
	}
	if t.Cost.Valid {
		if f, err := t.Cost.Float64Value(); err == nil {
			s := strconv.FormatFloat(f.Float64, 'f', -1, 64)
			resp.Cost = &s
		}
	}
	if t.Quality.Valid {
		q := int(t.Quality.Int32)
		resp.Quality = &q
	}
	if t.CooldownUntil.Valid {
		resp.CooldownUntil = timestampToString(t.CooldownUntil)
	}
	return resp
}

// modelTierInput is the shared create/update body. Pointers distinguish
// "omitted, keep the value" from "explicitly cleared". Cost arrives as a JSON
// number or numeric string and is canonicalized to plain decimal before
// binding, so forms Postgres ::numeric rejects never reach the query.
type modelTierInput struct {
	Name          *string `json:"name"`
	Idx           *int    `json:"idx"`
	RuntimeID     *string `json:"runtime_id"`
	Provider      *string `json:"provider"`
	Model         *string `json:"model"`
	ThinkingLevel *string `json:"thinking_level"`
	ServiceTier   *string `json:"service_tier"`
	Cost          *string `json:"cost"`
	Quality       *int    `json:"quality"`
	ProviderSlots *int    `json:"provider_slots"`
	// QualitySet tracks presence: omitted keeps the value, an explicit
	// null clears it to unknown (NULL), a number sets it (0 is legitimate).
	QualitySet bool  `json:"-"`
	Enabled    *bool `json:"enabled"`
	// Roles is a plain slice with a separate presence flag: an explicit []
	// clears the set (unconfigured), omitted keeps the value. "null" also
	// keeps (indistinguishable from omitted by choice, same as cost's "").
	Roles         []string `json:"roles"`
	RolesSet      bool     `json:"-"`
	ManualOnly    *bool    `json:"manual_only"`
	AutoPick      *bool    `json:"auto_pick"`
	QuotaKind     *string  `json:"quota_kind"`
	QuotaKinds    []string `json:"quota_kinds"`
	QuotaKindsSet bool     `json:"-"`
	CtxMaxTokens  *int     `json:"ctx_max_tokens"`
	CompactAtPct  *int     `json:"compact_at_pct"`
	// M-497: is_free is no longer an input. It is derived from quota_kinds
	// (free or free_daily selected) and kept on the column for the old binary.
}

func (in *modelTierInput) UnmarshalJSON(data []byte) error {
	type alias struct {
		Name          *string  `json:"name"`
		Idx           *int     `json:"idx"`
		RuntimeID     *string  `json:"runtime_id"`
		Provider      *string  `json:"provider"`
		Model         *string  `json:"model"`
		ThinkingLevel *string  `json:"thinking_level"`
		ServiceTier   *string  `json:"service_tier"`
		Cost          any      `json:"cost"`
		Quality       *int     `json:"quality"`
		ProviderSlots *int     `json:"provider_slots"`
		Enabled       *bool    `json:"enabled"`
		Roles         []string `json:"roles"`
		ManualOnly    *bool    `json:"manual_only"`
		AutoPick      *bool    `json:"auto_pick"`
		QuotaKind     *string  `json:"quota_kind"`
		QuotaKinds    []string `json:"quota_kinds"`
		CtxMaxTokens  *int     `json:"ctx_max_tokens"`
		CompactAtPct  *int     `json:"compact_at_pct"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	in.Name, in.Idx, in.RuntimeID = a.Name, a.Idx, a.RuntimeID
	in.Provider, in.Model = a.Provider, a.Model
	in.ThinkingLevel, in.ServiceTier = a.ThinkingLevel, a.ServiceTier
	in.Quality, in.ProviderSlots, in.Enabled = a.Quality, a.ProviderSlots, a.Enabled
	in.ManualOnly, in.AutoPick, in.QuotaKind = a.ManualOnly, a.AutoPick, a.QuotaKind
	in.CtxMaxTokens, in.CompactAtPct = a.CtxMaxTokens, a.CompactAtPct
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if raw, ok := probe["quality"]; ok {
		in.QualitySet = true
		if string(raw) == "null" {
			in.Quality = nil
		} else {
			in.Quality = a.Quality
		}
	}
	if raw, ok := probe["roles"]; ok && string(raw) != "null" {
		in.RolesSet = true
		in.Roles = a.Roles
	}
	if raw, ok := probe["quota_kinds"]; ok && string(raw) != "null" {
		in.QuotaKindsSet = true
		in.QuotaKinds = a.QuotaKinds
	}
	if a.Cost == nil {
		return nil
	}
	switch c := a.Cost.(type) {
	case string:
		s := strings.TrimSpace(c)
		if s == "" {
			in.Cost = nil
		} else {
			in.Cost = &s
		}
	case float64:
		s := strconv.FormatFloat(c, 'f', -1, 64)
		in.Cost = &s
	case json.Number:
		s := strings.TrimSpace(c.String())
		if s != "" {
			in.Cost = &s
		}
	default:
		return errors.New("cost must be a number")
	}
	return nil
}

func parseTierCost(raw *string) (pgtype.Numeric, error) {
	var out pgtype.Numeric
	if raw == nil {
		return out, nil
	}
	s := strings.TrimSpace(*raw)
	if s == "" {
		return out, nil
	}
	// Reject underscores (accepted by strconv but not a valid numeric literal here)
	// and any character outside a finite decimal/exponent number.
	for _, r := range s {
		if !(r == '+' || r == '-' || r == '.' || r == 'e' || r == 'E' || (r >= '0' && r <= '9')) {
			return out, errors.New("cost must be a finite number")
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return out, errors.New("cost must be a finite number")
	}
	if err := out.Scan(strconv.FormatFloat(f, 'f', -1, 64)); err != nil {
		return out, errors.New("cost must be a finite number")
	}
	return out, nil
}

// normalizeTierInput validates one create/update body. On create every field
// except thinking_level/service_tier/cost/quality is required; on update only
// present fields are validated against the existing row's values.
func normalizeTierInput(in modelTierInput, cur *db.ModelTier, forCreate bool) (db.CreateModelTierParams, *pgtype.UUID, error) {
	var params db.CreateModelTierParams
	if cur != nil {
		params.WorkspaceID = cur.WorkspaceID
		params.Name = cur.Name
		params.Idx = cur.Idx
		params.RuntimeID = cur.RuntimeID
		params.Provider = cur.Provider
		params.Model = cur.Model
		params.ThinkingLevel = cur.ThinkingLevel
		params.ServiceTier = cur.ServiceTier
		params.Cost = cur.Cost
		params.Quality = cur.Quality
		params.ProviderSlots = cur.ProviderSlots
		params.Enabled = cur.Enabled
		params.Roles = cur.Roles
		params.ManualOnly = pgtype.Bool{Bool: cur.ManualOnly, Valid: true}
		params.QuotaKind = pgtype.Text{String: cur.QuotaKind, Valid: true}
		params.IsFree = pgtype.Bool{Bool: cur.IsFree, Valid: true}
		params.AutoPick = pgtype.Bool{Bool: cur.AutoPick, Valid: true}
		params.QuotaKinds = append([]string{}, cur.QuotaKinds...)
		params.CtxMaxTokens = pgtype.Int4{Int32: cur.CtxMaxTokens, Valid: cur.CtxMaxTokens != 0}
		params.CompactAtPct = pgtype.Int4{Int32: cur.CompactAtPct, Valid: cur.CompactAtPct != 0}
	}

	name := params.Name
	if in.Name != nil || forCreate {
		n := name
		if in.Name != nil {
			n = strings.TrimSpace(*in.Name)
		}
		if err := validateTierName(n); err != nil {
			return params, nil, err
		}
		name = n
	}
	params.Name = name

	if in.Idx != nil {
		if *in.Idx < 0 || *in.Idx > 99 {
			return params, nil, errors.New("idx must be between 0 and 99")
		}
		params.Idx = int32(*in.Idx)
	} else if forCreate && in.Idx == nil {
		params.Idx = 99
	}

	var runtimeID *pgtype.UUID
	if in.RuntimeID != nil {
		s := strings.TrimSpace(*in.RuntimeID)
		if s == "" {
			params.RuntimeID = pgtype.UUID{}
		} else {
			if _, err := parseUUIDStrict(s); err != nil {
				return params, nil, errors.New("invalid runtime_id")
			}
			u := parseUUID(s)
			params.RuntimeID = u
			runtimeID = &u
		}
	} else if forCreate {
		params.RuntimeID = pgtype.UUID{}
	}

	if in.Provider != nil || forCreate {
		p := params.Provider
		if in.Provider != nil {
			p = strings.TrimSpace(*in.Provider)
		}
		if p == "" {
			return params, nil, errors.New("provider is required")
		}
		params.Provider = p
	}

	if in.Model != nil || forCreate {
		m := params.Model
		if in.Model != nil {
			m = strings.TrimSpace(*in.Model)
		}
		if m == "" {
			return params, nil, errors.New("model is required")
		}
		params.Model = m
	}

	if in.ThinkingLevel != nil {
		params.ThinkingLevel = optionalText(*in.ThinkingLevel)
	}
	if in.ServiceTier != nil {
		params.ServiceTier = optionalText(*in.ServiceTier)
	}
	if in.Cost != nil {
		cost, err := parseTierCost(in.Cost)
		if err != nil {
			return params, nil, err
		}
		params.Cost = cost
	}
	// QualitySet is authoritative on the JSON path (explicit null clears to
	// unknown, omitted keeps). Direct-constructed inputs (tests, non-JSON
	// callers) carry no flag, so a non-nil pointer also counts as set; only
	// an explicit JSON null reaches the clear branch.
	if in.QualitySet || in.Quality != nil {
		if in.Quality == nil {
			// Explicit null: clear to unknown (NULL). Omitted never reaches
			// here (QualitySet false), so keep-semantics are preserved, and
			// 0 still sets a legitimate measured floor via the branch below.
			params.Quality = pgtype.Int4{}
		} else {
			if *in.Quality < 0 || *in.Quality > 100 {
				return params, nil, errors.New("quality must be between 0 and 100")
			}
			params.Quality = pgtype.Int4{Int32: int32(*in.Quality), Valid: true}
		}
	}
	if in.ProviderSlots != nil {
		if *in.ProviderSlots < 1 || *in.ProviderSlots > 1000 {
			return params, nil, errors.New("provider_slots must be at least 1")
		}
		params.ProviderSlots = int32(*in.ProviderSlots)
	} else if forCreate && in.ProviderSlots == nil {
		params.ProviderSlots = 1
	}
	if in.Enabled != nil {
		params.Enabled = *in.Enabled
	}
	if in.RolesSet {
		for _, r := range in.Roles {
			if !validTierRoleName(r) {
				return params, nil, errors.New("roles must be lowercase letters, digits or hyphens, up to 40 chars")
			}
		}
		// M-497: store the canonical set (legacy mapped, deduped, sorted) so
		// model_tier.roles stays derived from the per-role rows.
		params.Roles = canonicalTierRoles(in.Roles)
	} else if forCreate {
		params.Roles = []string{}
	}
	// ManualOnly/QuotaKind ride sqlc.narg NULLABLE params: present →
	// explicit (even false), omitted → invalid → COALESCE keeps the column
	// default (create) or the row value (update). IsFree is always derived
	// from quota kinds below (M-497).
	if in.ManualOnly != nil {
		params.ManualOnly = pgtype.Bool{Bool: *in.ManualOnly, Valid: true}
	}
	if in.AutoPick != nil {
		params.AutoPick = pgtype.Bool{Bool: *in.AutoPick, Valid: true}
	}
	// Keep auto_pick/manual_only in sync (M-481: auto_pick replaces manual_only).
	if in.AutoPick != nil {
		params.ManualOnly = pgtype.Bool{Bool: !*in.AutoPick, Valid: true}
	} else if in.ManualOnly != nil {
		params.AutoPick = pgtype.Bool{Bool: !*in.ManualOnly, Valid: true}
	} else if forCreate && !params.AutoPick.Valid {
		params.AutoPick = pgtype.Bool{Bool: true, Valid: true}
		params.ManualOnly = pgtype.Bool{Bool: false, Valid: true}
	}
	if in.QuotaKind != nil {
		q := strings.TrimSpace(*in.QuotaKind)
		switch q {
		case "daily", "monthly", "five_hour_weekly", "none":
			params.QuotaKind = pgtype.Text{String: q, Valid: true}
		default:
			return params, nil, errors.New("quota_kind must be daily, monthly, five_hour_weekly or none")
		}
	}
	if in.QuotaKindsSet {
		seen := map[string]struct{}{}
		out := make([]string, 0, len(in.QuotaKinds))
		for _, qk := range in.QuotaKinds {
			qk = strings.TrimSpace(qk)
			if qk == "" {
				continue
			}
			if !validTierQuotaKind(qk) {
				return params, nil, errors.New("quota_kinds must be a subset of free_daily, free, local, quota_5_hours, quota_daily, quota_weekly, quota_monthly")
			}
			if _, dup := seen[qk]; dup {
				continue
			}
			seen[qk] = struct{}{}
			out = append(out, qk)
		}
		params.QuotaKinds = out
		// Best-effort legacy scalar for old binaries.
		params.QuotaKind = pgtype.Text{String: legacyQuotaKindFromKinds(out), Valid: true}
	} else if forCreate && params.QuotaKinds == nil {
		params.QuotaKinds = []string{}
	}
	// M-497: is_free is derived from the resulting quota kinds (free or
	// free_daily selected) and kept on the column for the old binary.
	params.IsFree = pgtype.Bool{Bool: tierIsFreeFromKinds(params.QuotaKinds), Valid: true}
	if in.CtxMaxTokens != nil {
		if *in.CtxMaxTokens < 1024 || *in.CtxMaxTokens > 2000000 {
			return params, nil, errors.New("ctx_max_tokens must be between 1024 and 2000000")
		}
		params.CtxMaxTokens = pgtype.Int4{Int32: int32(*in.CtxMaxTokens), Valid: true}
	} else if forCreate && !params.CtxMaxTokens.Valid {
		params.CtxMaxTokens = pgtype.Int4{Int32: 280000, Valid: true}
	}
	if in.CompactAtPct != nil {
		if *in.CompactAtPct < 1 || *in.CompactAtPct > 100 {
			return params, nil, errors.New("compact_at_pct must be between 1 and 100")
		}
		params.CompactAtPct = pgtype.Int4{Int32: int32(*in.CompactAtPct), Valid: true}
	} else if forCreate && !params.CompactAtPct.Valid {
		params.CompactAtPct = pgtype.Int4{Int32: 75, Valid: true}
	}
	return params, runtimeID, nil
}

// validTierRoleName reports whether r is a usable role key (M-497, Owner
// Q4): roles are free workspace-defined keys, so only the slug shape is
// enforced — lowercase letters, digits, hyphens, up to 40 chars, starting
// with a letter or digit. The old fixed vocabulary CHECKs are gone (746);
// the tier_role registry (744) is the workspace's role set.
func validTierRoleName(r string) bool {
	return tierRoleKeyPattern.MatchString(r)
}

// tierRoleKeyPattern constrains role keys so they survive mentions, URLs and
// tab params. Shared by tier roles, agent roles and the registry.
var tierRoleKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// canonicalTierRole maps the four legacy M-481 role names onto the three
// classic roles (migration 740 did the same for stored rows). Custom
// workspace-defined keys pass through unchanged.
func canonicalTierRole(r string) string {
	switch r {
	case "leader", "reviewer", "orchestrator":
		return "management"
	case "planning":
		return "architect"
	default:
		return r
	}
}

// validTierRoleKey reports whether r is a usable per-role-list key: any
// slug-shaped role, classic or workspace-defined (M-497, Owner Q4). ?role=
// and role reorder accept these; unknown keys simply match no tiers.
func validTierRoleKey(r string) bool {
	return validTierRoleName(r)
}

// canonicalTierRoles normalizes a roles set: legacy names mapped, blank or
// malformed keys dropped, deduped, sorted. Custom keys are kept as-is. The
// result is what model_tier.roles stores and what model_tier_role rows (and
// tier_role registry entries) are kept for.
func canonicalTierRoles(roles []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, r := range roles {
		c := canonicalTierRole(strings.TrimSpace(r))
		if !validTierRoleKey(c) {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// tierIsFreeFromKinds derives the legacy is_free flag from quota kinds
// (M-497): a tier counts as free when a free kind is selected.
func tierIsFreeFromKinds(kinds []string) bool {
	for _, k := range kinds {
		if k == "free" || k == "free_daily" {
			return true
		}
	}
	return false
}

func validTierQuotaKind(k string) bool {
	switch k {
	case "free_daily", "free", "local", "quota_5_hours", "quota_daily", "quota_weekly", "quota_monthly":
		return true
	}
	return false
}

func legacyQuotaKindFromKinds(kinds []string) string {
	has := map[string]bool{}
	for _, k := range kinds {
		has[k] = true
	}
	if has["quota_5_hours"] || has["quota_weekly"] {
		return "five_hour_weekly"
	}
	if has["quota_daily"] {
		return "daily"
	}
	if has["quota_monthly"] {
		return "monthly"
	}
	return "none"
}

// tierManualProtectionWeakened reports whether an update drops manual-only
// protection that a non-owner must not touch: clearing manual_only, or
// narrowing roles while manual-only stays set (roles gate who the tier can
// serve, so shrinking them on a manual tier reshapes the owner-only lane).
// Disabling the tier is NOT weakening (it removes it from every path).
func tierManualProtectionWeakened(cur db.ModelTier, nextManualOnly pgtype.Bool, rolesSet bool, nextRoles []string) bool {
	if !cur.ManualOnly {
		return false
	}
	// An explicit false clears protection; omitted (invalid → COALESCE keeps
	// true) does not.
	if nextManualOnly.Valid && !nextManualOnly.Bool {
		return true
	}
	if rolesSet && len(nextRoles) < len(cur.Roles) {
		return true
	}
	return false
}

// modelTierRuntimeInWorkspace reports whether runtimeID names a runtime in
// this workspace. Tiers stay usable when the runtime is offline or deleted —
// M-351 skips such tiers at resolve time — but a tier must never point at
// another workspace's runtime.
func (h *Handler) modelTierRuntimeInWorkspace(r *http.Request, wsUUID pgtype.UUID, runtimeID pgtype.UUID) bool {
	if !runtimeID.Valid {
		return true
	}
	var count int
	err := h.DB.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM agent_runtime WHERE id = $1 AND workspace_id = $2`,
		runtimeID, wsUUID).Scan(&count)
	return err == nil && count == 1
}

// defaultTierRoles is the classic role set a workspace starts with (M-498).
// Migration 744 seeds it for workspaces that already existed; a workspace
// created later is seeded by CreateWorkspace, so Tier config always opens on
// real tabs even before its first tier is written.
var defaultTierRoles = []string{"architect", "management", "engineer"}

// seedTierRoles writes the classic roles into a workspace's registry
// (M-498). Idempotent: an existing key keeps its position.
func seedTierRoles(ctx context.Context, q *db.Queries, wsUUID pgtype.UUID) error {
	for pos, key := range defaultTierRoles {
		if err := q.InsertTierRoleIgnore(ctx, db.InsertTierRoleIgnoreParams{
			WorkspaceID: wsUUID, Key: key, Position: int32(pos),
		}); err != nil {
			return err
		}
	}
	return nil
}

// ensureTierRole registers a role key in the workspace registry (M-497,
// Owner Q4), appending unknown keys at the end. Concurrent writers may tie
// on position; the registry list breaks ties by key.
func (h *Handler) ensureTierRole(r *http.Request, wsUUID pgtype.UUID, key string) {
	maxPos, err := h.Queries.MaxTierRolePosition(r.Context(), wsUUID)
	if err != nil {
		// A missing registry row is not worth failing the tier write, but it is
		// worth knowing about: the role would otherwise vanish from the tabs.
		slog.WarnContext(r.Context(), "tier role registry: position read failed",
			"workspace_id", uuidToString(wsUUID), "role", key, "error", err)
		return
	}
	if err := h.Queries.InsertTierRoleIgnore(r.Context(), db.InsertTierRoleIgnoreParams{
		WorkspaceID: wsUUID, Key: key, Position: maxPos + 1,
	}); err != nil {
		slog.WarnContext(r.Context(), "tier role registry: register failed",
			"workspace_id", uuidToString(wsUUID), "role", key, "error", err)
	}
}

// syncTierRoleRows reconciles a tier's model_tier_role rows with its
// canonical roles (M-497): rows for dropped roles are removed, rows for kept
// roles are left at their position, and rows for newly added roles are
// appended at the end of that role's list. Returns the tier's role→idx map.
func (h *Handler) syncTierRoleRows(r *http.Request, wsUUID pgtype.UUID, tierID pgtype.UUID, roles []string) map[string]int32 {
	roleIdx := map[string]int32{}
	// Drop rows for removed roles (empty roles drop every row: the tier
	// leaves every role list but keeps matching legacy role-less agents).
	if err := h.Queries.DeleteModelTierRolesNotIn(r.Context(), db.DeleteModelTierRolesNotInParams{
		TierID: tierID, Roles: roles,
	}); err != nil {
		slog.WarnContext(r.Context(), "tier role rows: drop failed",
			"workspace_id", uuidToString(wsUUID), "tier_id", uuidToString(tierID), "error", err)
	}
	for _, role := range roles {
		h.ensureTierRole(r, wsUUID, role)
	}
	for _, role := range roles {
		rows, err := h.Queries.ListModelTierRoleIdxForTiers(r.Context(), db.ListModelTierRoleIdxForTiersParams{
			Ids: []pgtype.UUID{tierID}, Role: role,
		})
		if err == nil && len(rows) > 0 {
			roleIdx[role] = rows[0].Idx
			continue
		}
		maxIdx, err := h.Queries.MaxModelTierRoleIdx(r.Context(), db.MaxModelTierRoleIdxParams{
			WorkspaceID: wsUUID, Role: role,
		})
		if err != nil {
			slog.WarnContext(r.Context(), "tier role rows: max idx read failed",
				"workspace_id", uuidToString(wsUUID), "tier_id", uuidToString(tierID),
				"role", role, "error", err)
			continue
		}
		row, err := h.Queries.UpsertModelTierRole(r.Context(), db.UpsertModelTierRoleParams{
			WorkspaceID: wsUUID, TierID: tierID, Role: role, Idx: maxIdx + 1,
		})
		if err != nil {
			slog.WarnContext(r.Context(), "tier role rows: append failed",
				"workspace_id", uuidToString(wsUUID), "tier_id", uuidToString(tierID),
				"role", role, "error", err)
			continue
		}
		roleIdx[role] = row.Idx
	}
	return roleIdx
}

// tierRoleIdxMap loads every (tier → idx) in this workspace for one role.
func (h *Handler) tierRoleIdxMap(r *http.Request, wsUUID pgtype.UUID, role string) map[string]int32 {
	out := map[string]int32{}
	rows, err := h.Queries.ListModelTierRolesByWorkspace(r.Context(), wsUUID)
	if err != nil {
		return out
	}
	for _, row := range rows {
		if row.Role != role {
			continue
		}
		out[uuidToString(row.TierID)] = row.Idx
	}
	return out
}

// withRoleIdx stamps role_idx onto tier responses for a role-filtered list.
func withRoleIdx(tiers []ModelTierResponse, roleIdx map[string]int32) []ModelTierResponse {
	for i := range tiers {
		if idx, ok := roleIdx[tiers[i].ID]; ok {
			v := int(idx)
			tiers[i].RoleIdx = &v
		}
	}
	return tiers
}

// ListModelTiers returns the workspace's tiers ordered by idx. Member-visible:
// the payload names runtimes and models only, and M-351 agent pickers need it.
// With ?role=<key> (M-497) only tiers in that role's list are returned,
// ordered by the role-specific idx, each carrying its role_idx. Keys are
// free workspace-defined strings (Owner Q4); an unknown key matches nothing.
// A tier appears in a role's list only if it has a row for that role;
// legacy tiers with no rows match only the unfiltered list.
func (h *Handler) ListModelTiers(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	roleFilter := strings.TrimSpace(r.URL.Query().Get("role"))
	if roleFilter != "" && !validTierRoleKey(roleFilter) {
		writeError(w, http.StatusBadRequest, "role must be lowercase letters, digits or hyphens, up to 40 chars")
		return
	}
	tiers, err := h.Queries.ListModelTiers(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tiers")
		return
	}
	out := make([]ModelTierResponse, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, modelTierToResponse(t))
	}
	if roleFilter != "" {
		roleIdx := h.tierRoleIdxMap(r, wsUUID, roleFilter)
		filtered := out[:0]
		for _, t := range out {
			if _, ok := roleIdx[t.ID]; ok {
				filtered = append(filtered, t)
			}
		}
		out = filtered
		// Role-specific order (then creation order for stability).
		sort.SliceStable(out, func(i, j int) bool {
			ii, jj := roleIdx[out[i].ID], roleIdx[out[j].ID]
			if ii != jj {
				return ii < jj
			}
			return out[i].CreatedAt < out[j].CreatedAt
		})
		out = withRoleIdx(out, roleIdx)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiers": out})
}

// TierRoleResponse is one entry of the workspace role registry (M-497).
type TierRoleResponse struct {
	Key      string `json:"key"`
	Position int    `json:"position"`
}

// ListTierRoles returns the workspace's role registry in tab order
// (M-497, Owner Q4): the seeded classic roles plus every workspace-defined
// key tier writes have registered. Member-visible: the Settings tabs and
// tier pickers read it.
func (h *Handler) ListTierRoles(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListTierRolesByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list roles")
		return
	}
	out := make([]TierRoleResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, TierRoleResponse{Key: row.Key, Position: int(row.Position)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// tierRoleList is the registry as the API returns it.
func (h *Handler) tierRoleList(w http.ResponseWriter, r *http.Request, wsUUID pgtype.UUID) {
	rows, err := h.Queries.ListTierRolesByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list roles")
		return
	}
	out := make([]TierRoleResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, TierRoleResponse{Key: row.Key, Position: int(row.Position)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out})
}

// CreateTierRole adds a role to the workspace registry (M-498, owner/admin).
// Roles are free workspace-defined keys; the tabs are built from this list, so
// an admin adds, renames and removes them here instead of editing a tier.
// Idempotent on the key: registering an existing role returns 409 so the tab
// list cannot grow a duplicate row.
func (h *Handler) CreateTierRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key := canonicalTierRole(strings.TrimSpace(in.Key))
	if !validTierRoleKey(key) {
		writeError(w, http.StatusBadRequest, "role must be lowercase letters, digits or hyphens, up to 40 chars")
		return
	}
	maxPos, err := h.Queries.MaxTierRolePosition(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read roles")
		return
	}
	if _, err := h.Queries.CreateTierRole(r.Context(), db.CreateTierRoleParams{
		WorkspaceID: wsUUID, Key: key, Position: maxPos + 1,
	}); err != nil {
		if isTierNameConflict(err) {
			writeError(w, http.StatusConflict, "this role already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create role")
		return
	}
	h.tierRoleList(w, r, wsUUID)
}

// RenameTierRole renames a registry key (M-498, owner/admin). The tier rows
// move with it — model_tier_role is the source of truth for a tier's roles and
// per-role order, and model_tier.roles stays in step for the old binary — so a
// renamed role keeps every tier it had instead of emptying its tab.
func (h *Handler) RenameTierRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	oldKey := canonicalTierRole(strings.TrimSpace(chi.URLParam(r, "roleKey")))
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	newKey := canonicalTierRole(strings.TrimSpace(in.Key))
	if !validTierRoleKey(newKey) {
		writeError(w, http.StatusBadRequest, "role must be lowercase letters, digits or hyphens, up to 40 chars")
		return
	}
	if _, err := h.Queries.GetTierRole(r.Context(), db.GetTierRoleParams{WorkspaceID: wsUUID, Key: oldKey}); err != nil {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	if newKey != oldKey {
		if _, err := h.Queries.GetTierRole(r.Context(), db.GetTierRoleParams{WorkspaceID: wsUUID, Key: newKey}); err == nil {
			writeError(w, http.StatusConflict, "this role already exists")
			return
		}
		if rows, err := h.Queries.RenameTierRole(r.Context(), db.RenameTierRoleParams{
			WorkspaceID: wsUUID, OldKey: oldKey, NewKey: newKey,
		}); err != nil || rows == 0 {
			if isTierNameConflict(err) {
				writeError(w, http.StatusConflict, "this role already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to rename role")
			return
		}
		if err := h.Queries.RenameModelTierRoleKey(r.Context(), db.RenameModelTierRoleKeyParams{
			WorkspaceID: wsUUID, OldKey: oldKey, NewKey: newKey,
		}); err != nil {
			slog.ErrorContext(r.Context(), "tier role rename: per-role rows not moved",
				"workspace_id", uuidToString(wsUUID), "from", oldKey, "to", newKey, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to rename role")
			return
		}
		if err := h.Queries.RenameModelTierRoleInTiers(r.Context(), db.RenameModelTierRoleInTiersParams{
			WorkspaceID: wsUUID, OldKey: oldKey, NewKey: newKey,
		}); err != nil {
			slog.WarnContext(r.Context(), "tier role rename: legacy roles array not updated",
				"workspace_id", uuidToString(wsUUID), "from", oldKey, "to", newKey, "error", err)
		}
	}
	h.tierRoleList(w, r, wsUUID)
}

// DeleteTierRole removes a registry key (M-498, owner/admin). Refused while a
// tier still carries the role: deleting it would leave those tiers pointing at
// a list that no longer exists, and the admin can move them first.
func (h *Handler) DeleteTierRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	key := canonicalTierRole(strings.TrimSpace(chi.URLParam(r, "roleKey")))
	inUse, err := h.Queries.CountTiersUsingRole(r.Context(), db.CountTiersUsingRoleParams{
		WorkspaceID: wsUUID, Role: key,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read roles")
		return
	}
	if inUse > 0 {
		writeErrorCode(w, http.StatusConflict, "role_in_use", "role is used by a tier")
		return
	}
	rows, err := h.Queries.DeleteTierRole(r.Context(), db.DeleteTierRoleParams{WorkspaceID: wsUUID, Key: key})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete role")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "role not found")
		return
	}
	h.tierRoleList(w, r, wsUUID)
}

// CreateModelTier adds a tier (owner/admin).
func (h *Handler) CreateModelTier(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var in modelTierInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params, runtimeID, err := normalizeTierInput(in, nil, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	params.WorkspaceID = wsUUID
	if runtimeID != nil && !h.modelTierRuntimeInWorkspace(r, wsUUID, *runtimeID) {
		writeError(w, http.StatusBadRequest, "runtime is not in this workspace")
		return
	}
	created, err := h.Queries.CreateModelTier(r.Context(), params)
	if err != nil {
		if isTierNameConflict(err) {
			writeError(w, http.StatusConflict, "a tier with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create tier")
		return
	}
	// M-481: backfill a first member so the tier is immediately resolvable by
	// the member-aware resolver. Legacy tier columns remain in sync.
	_, _ = h.Queries.CreateModelTierMember(r.Context(), db.CreateModelTierMemberParams{
		TierID:        created.ID,
		Position:      0,
		Provider:      created.Provider,
		Model:         created.Model,
		ThinkingLevel: created.ThinkingLevel,
		RuntimeID:     created.RuntimeID,
		ProviderSlots: created.ProviderSlots,
		Enabled:       created.Enabled,
	})
	// M-497: new role rows go at the end of each role's list.
	h.syncTierRoleRows(r, wsUUID, created.ID, created.Roles)
	writeJSON(w, http.StatusCreated, modelTierToResponse(created))
}

// UpdateModelTier edits a tier (owner/admin). Omitted fields keep their value;
// an explicit empty runtime_id unbinds the runtime.
// M-423 anti-clear: a non-owner (admin) edit that would drop manual-only
// protection — clearing manual_only or narrowing roles away while manual-only
// stays set — is rejected with 403. Hiding the control in the UI is not
// sufficient; the server enforces it.
func (h *Handler) UpdateModelTier(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	caller, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tierUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "tierId")), "tier id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{
		ID: tierUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
		return
	}
	var in modelTierInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params, runtimeID, err := normalizeTierInput(in, &existing, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if caller.Role != "owner" && tierManualProtectionWeakened(existing, params.ManualOnly, in.RolesSet, in.Roles) {
		writeError(w, http.StatusForbidden, "only the workspace owner can lift manual-only protection")
		return
	}
	if runtimeID != nil && !h.modelTierRuntimeInWorkspace(r, wsUUID, *runtimeID) {
		writeError(w, http.StatusBadRequest, "runtime is not in this workspace")
		return
	}
	updated, err := h.Queries.UpdateModelTier(r.Context(), db.UpdateModelTierParams{
		ID:            tierUUID,
		Name:          params.Name,
		Idx:           params.Idx,
		RuntimeID:     params.RuntimeID,
		Provider:      params.Provider,
		Model:         params.Model,
		ThinkingLevel: params.ThinkingLevel,
		ServiceTier:   params.ServiceTier,
		Cost:          params.Cost,
		Quality:       params.Quality,
		ProviderSlots: params.ProviderSlots,
		Enabled:       params.Enabled,
		Roles:         params.Roles,
		ManualOnly:    params.ManualOnly,
		QuotaKind:     params.QuotaKind,
		IsFree:        params.IsFree,
		AutoPick:      params.AutoPick,
		QuotaKinds:    params.QuotaKinds,
		CtxMaxTokens:  params.CtxMaxTokens,
		CompactAtPct:  params.CompactAtPct,
	})
	if err != nil {
		if isTierNameConflict(err) {
			writeError(w, http.StatusConflict, "a tier with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update tier")
		return
	}
	// M-481: keep a single backfilled member in sync for legacy API callers.
	// If multiple members exist, members are the source of truth.
	if members, merr := h.Queries.ListModelTierMembers(r.Context(), tierUUID); merr == nil {
		if len(members) == 1 {
			m := members[0]
			_, _ = h.Queries.UpdateModelTierMember(r.Context(), db.UpdateModelTierMemberParams{
				ID:            m.ID,
				Position:      m.Position,
				Provider:      updated.Provider,
				Model:         updated.Model,
				ThinkingLevel: updated.ThinkingLevel,
				RuntimeID:     updated.RuntimeID,
				ProviderSlots: updated.ProviderSlots,
				CooldownUntil: m.CooldownUntil,
				Enabled:       updated.Enabled,
			})
		} else if len(members) > 1 {
			h.syncTierLegacyFromMembers(r, updated)
		}
	}
	// M-497: keep the per-role rows in sync when roles change. Kept roles
	// hold their position; added roles append at the end of their list.
	if in.RolesSet {
		h.syncTierRoleRows(r, wsUUID, tierUUID, updated.Roles)
		// New rows have no position yet for other roles — reload not needed:
		// role_idx is only stamped on role-filtered reads.
	}
	writeJSON(w, http.StatusOK, modelTierToResponse(updated))
}

// DeleteModelTier removes a tier (owner/admin).
func (h *Handler) DeleteModelTier(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	caller, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tierUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "tierId")), "tier id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{
		ID: tierUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
		return
	}
	// M-423: deleting a manual-only tier drops owner-only protection — owner
	// only (admin gets 403).
	if existing.ManualOnly && caller.Role != "owner" {
		writeError(w, http.StatusForbidden, "only the workspace owner can delete a manual-only tier")
		return
	}
	// M-351: a deleted tier stays selected on its agents as "unavailable"
	// (never silently dropped), but the join rows must go — ListAgentTiers
	// joins model_tier, so orphan rows would vanish from reads anyway while
	// leaving dead weight. No FK per repo rule; app-layer cleanup here.
	if err := h.Queries.DeleteTierAgentSelections(r.Context(), tierUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete tier")
		return
	}
	if err := h.Queries.DeleteModelTierMembersByTier(r.Context(), tierUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete tier")
		return
	}
	// M-497: drop the per-role order rows with the tier.
	if err := h.Queries.DeleteModelTierRolesByTier(r.Context(), tierUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete tier")
		return
	}
	if err := h.Queries.DeleteModelTier(r.Context(), tierUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete tier")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ReorderModelTiers replaces the tier order (owner/admin). The body is the
// full ordered id list; position becomes the new idx. Ids outside this
// workspace are rejected before anything is written.
//
// With {"role", "ids"} (M-497) only that role's list is rewritten —
// reordering Engineer never moves Architect — and the response carries
// role_idx for the reordered role. Role keys are free workspace-defined
// strings (Owner Q4); reordering registers an unknown key. Without "role"
// the legacy global idx order is rewritten (old binary compat).
func (h *Handler) ReorderModelTiers(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var req struct {
		IDs  []string `json:"ids"`
		Role string   `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 100 {
		writeError(w, http.StatusBadRequest, "ids must list 1 to 100 tiers")
		return
	}
	role := strings.TrimSpace(req.Role)
	if role != "" {
		if !validTierRoleKey(role) {
			writeError(w, http.StatusBadRequest, "role must be lowercase letters, digits or hyphens, up to 40 chars")
			return
		}
		h.ensureTierRole(r, wsUUID, role)
	}
	seen := make(map[string]struct{}, len(req.IDs))
	uuids := make([]pgtype.UUID, 0, len(req.IDs))
	for _, s := range req.IDs {
		s = strings.TrimSpace(s)
		if _, dup := seen[s]; dup {
			writeError(w, http.StatusBadRequest, "ids must not repeat a tier")
			return
		}
		seen[s] = struct{}{}
		u, ok := parseUUIDOrBadRequest(w, s, "tier id")
		if !ok {
			return
		}
		uuids = append(uuids, u)
	}
	existing, err := h.Queries.ListModelTiers(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tiers")
		return
	}
	owned := make(map[string]struct{}, len(existing))
	for _, t := range existing {
		owned[uuidToString(t.ID)] = struct{}{}
	}
	for _, s := range req.IDs {
		if _, ok := owned[strings.TrimSpace(s)]; !ok {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
	}
	if role != "" {
		// A role reorder may only reorder that role's own list (M-498 review):
		// an id outside it would silently join the role through the upsert,
		// which is the tier editor's job, not a drag of another tab's rows.
		roleIdxBefore := h.tierRoleIdxMap(r, wsUUID, role)
		for _, id := range uuids {
			if _, ok := roleIdxBefore[uuidToString(id)]; !ok {
				writeError(w, http.StatusBadRequest, "ids must list tiers of this role only")
				return
			}
		}
		for pos, id := range uuids {
			if _, err := h.Queries.UpsertModelTierRole(r.Context(), db.UpsertModelTierRoleParams{
				WorkspaceID: wsUUID, TierID: id, Role: role, Idx: int32(pos),
			}); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to reorder tiers")
				return
			}
		}
		tiers, err := h.Queries.ListModelTiers(r.Context(), wsUUID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list tiers")
			return
		}
		roleIdx := h.tierRoleIdxMap(r, wsUUID, role)
		out := make([]ModelTierResponse, 0, len(tiers))
		for _, t := range tiers {
			if _, ok := roleIdx[uuidToString(t.ID)]; !ok {
				continue
			}
			out = append(out, modelTierToResponse(t))
		}
		sort.SliceStable(out, func(i, j int) bool {
			ii, jj := roleIdx[out[i].ID], roleIdx[out[j].ID]
			if ii != jj {
				return ii < jj
			}
			return out[i].CreatedAt < out[j].CreatedAt
		})
		writeJSON(w, http.StatusOK, map[string]any{"tiers": withRoleIdx(out, roleIdx)})
		return
	}
	if err := h.Queries.ReorderModelTiers(r.Context(), db.ReorderModelTiersParams{
		Ids:         uuids,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reorder tiers")
		return
	}
	tiers, err := h.Queries.ListModelTiers(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tiers")
		return
	}
	out := make([]ModelTierResponse, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, modelTierToResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tiers": out})
}

// ── Model tier members (M-481) ───────────────────────────────────────────────

type ModelTierMemberResponse struct {
	ID            string `json:"id"`
	TierID        string `json:"tier_id"`
	Position      int    `json:"position"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinking_level,omitempty"`
	RuntimeID     string `json:"runtime_id,omitempty"`
	ProviderSlots int    `json:"provider_slots"`
	CooldownUntil string `json:"cooldown_until,omitempty"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func modelTierMemberToResponse(m db.ModelTierMember) ModelTierMemberResponse {
	resp := ModelTierMemberResponse{
		ID:            uuidToString(m.ID),
		TierID:        uuidToString(m.TierID),
		Position:      int(m.Position),
		Provider:      m.Provider,
		Model:         m.Model,
		ProviderSlots: int(m.ProviderSlots),
		Enabled:       m.Enabled,
		CreatedAt:     timestampToString(m.CreatedAt),
		UpdatedAt:     timestampToString(m.UpdatedAt),
	}
	if m.ThinkingLevel.Valid {
		resp.ThinkingLevel = m.ThinkingLevel.String
	}
	if m.RuntimeID.Valid {
		resp.RuntimeID = uuidToString(m.RuntimeID)
	}
	if m.CooldownUntil.Valid {
		resp.CooldownUntil = timestampToString(m.CooldownUntil)
	}
	return resp
}

type modelTierMemberInput struct {
	Position      *int    `json:"position"`
	Provider      *string `json:"provider"`
	Model         *string `json:"model"`
	ThinkingLevel *string `json:"thinking_level"`
	RuntimeID     *string `json:"runtime_id"`
	ProviderSlots *int    `json:"provider_slots"`
	Enabled       *bool   `json:"enabled"`
}

func normalizeTierMemberInput(in modelTierMemberInput, cur *db.ModelTierMember, forCreate bool) (db.CreateModelTierMemberParams, *pgtype.UUID, error) {
	var params db.CreateModelTierMemberParams
	if cur != nil {
		params.TierID = cur.TierID
		params.Position = cur.Position
		params.Provider = cur.Provider
		params.Model = cur.Model
		params.ThinkingLevel = cur.ThinkingLevel
		params.RuntimeID = cur.RuntimeID
		params.ProviderSlots = cur.ProviderSlots
		params.CooldownUntil = cur.CooldownUntil
		params.Enabled = cur.Enabled
	}
	if in.Position != nil {
		if *in.Position < 0 {
			return params, nil, errors.New("position must be >= 0")
		}
		params.Position = int32(*in.Position)
	} else if forCreate {
		params.Position = 0
	}
	if in.Provider != nil || forCreate {
		p := strings.TrimSpace(params.Provider)
		if in.Provider != nil {
			p = strings.TrimSpace(*in.Provider)
		}
		if p == "" {
			return params, nil, errors.New("provider is required")
		}
		params.Provider = p
	}
	if in.Model != nil || forCreate {
		m := strings.TrimSpace(params.Model)
		if in.Model != nil {
			m = strings.TrimSpace(*in.Model)
		}
		if m == "" {
			return params, nil, errors.New("model is required")
		}
		params.Model = m
	}
	if in.ThinkingLevel != nil {
		params.ThinkingLevel = optionalText(*in.ThinkingLevel)
	}
	var runtimeID *pgtype.UUID
	if in.RuntimeID != nil {
		s := strings.TrimSpace(*in.RuntimeID)
		if s == "" {
			params.RuntimeID = pgtype.UUID{}
		} else {
			if _, err := parseUUIDStrict(s); err != nil {
				return params, nil, errors.New("invalid runtime_id")
			}
			u := parseUUID(s)
			params.RuntimeID = u
			runtimeID = &u
		}
	} else if forCreate {
		params.RuntimeID = pgtype.UUID{}
	}
	if in.ProviderSlots != nil {
		if *in.ProviderSlots < 1 || *in.ProviderSlots > 1000 {
			return params, nil, errors.New("provider_slots must be at least 1")
		}
		params.ProviderSlots = int32(*in.ProviderSlots)
	} else if forCreate {
		params.ProviderSlots = 1
	}
	if in.Enabled != nil {
		params.Enabled = *in.Enabled
	} else if forCreate {
		params.Enabled = true
	}
	return params, runtimeID, nil
}

func (h *Handler) syncTierLegacyFromMembers(r *http.Request, tier db.ModelTier) {
	members, err := h.Queries.ListModelTierMembers(r.Context(), tier.ID)
	if err != nil || len(members) == 0 {
		return
	}
	// First enabled member in position order, else first member.
	pick := members[0]
	for _, m := range members {
		if m.Enabled {
			pick = m
			break
		}
	}
	_, _ = h.Queries.UpdateModelTier(r.Context(), db.UpdateModelTierParams{
		ID:            tier.ID,
		Name:          tier.Name,
		Idx:           tier.Idx,
		RuntimeID:     pick.RuntimeID,
		Provider:      pick.Provider,
		Model:         pick.Model,
		ThinkingLevel: pick.ThinkingLevel,
		ServiceTier:   tier.ServiceTier,
		Cost:          tier.Cost,
		Quality:       tier.Quality,
		ProviderSlots: pick.ProviderSlots,
		Enabled:       tier.Enabled,
		Roles:         tier.Roles,
		ManualOnly:    pgtype.Bool{Bool: tier.ManualOnly, Valid: true},
		QuotaKind:     pgtype.Text{String: tier.QuotaKind, Valid: true},
		IsFree:        pgtype.Bool{Bool: tier.IsFree, Valid: true},
		AutoPick:      pgtype.Bool{Bool: tier.AutoPick, Valid: true},
		QuotaKinds:    tier.QuotaKinds,
		CtxMaxTokens:  pgtype.Int4{Int32: tier.CtxMaxTokens, Valid: tier.CtxMaxTokens != 0},
		CompactAtPct:  pgtype.Int4{Int32: tier.CompactAtPct, Valid: tier.CompactAtPct != 0},
	})
	// Sync tier cooldown from picked member for legacy readers.
	_ = h.Queries.SetModelTierCooldown(r.Context(), db.SetModelTierCooldownParams{ID: tier.ID, CooldownUntil: pick.CooldownUntil})
}

func (h *Handler) ListModelTierMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tierUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "tierId")), "tier id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{ID: tierUUID, WorkspaceID: wsUUID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
		return
	}
	members, err := h.Queries.ListModelTierMembers(r.Context(), tierUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tier members")
		return
	}
	out := make([]ModelTierMemberResponse, 0, len(members))
	for _, m := range members {
		out = append(out, modelTierMemberToResponse(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

func (h *Handler) CreateModelTierMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tierUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "tierId")), "tier id")
	if !ok {
		return
	}
	tier, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{ID: tierUUID, WorkspaceID: wsUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
		return
	}
	var in modelTierMemberInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params, runtimeID, err := normalizeTierMemberInput(in, nil, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	params.TierID = tierUUID
	if runtimeID != nil && !h.modelTierRuntimeInWorkspace(r, wsUUID, *runtimeID) {
		writeError(w, http.StatusBadRequest, "runtime is not in this workspace")
		return
	}
	created, err := h.Queries.CreateModelTierMember(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create tier member")
		return
	}
	h.syncTierLegacyFromMembers(r, tier)
	writeJSON(w, http.StatusCreated, modelTierMemberToResponse(created))
}

func (h *Handler) UpdateModelTierMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tierUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "tierId")), "tier id")
	if !ok {
		return
	}
	memberUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "memberId")), "member id")
	if !ok {
		return
	}
	tier, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{ID: tierUUID, WorkspaceID: wsUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
		return
	}
	existing, err := h.Queries.GetModelTierMemberInTier(r.Context(), db.GetModelTierMemberInTierParams{ID: memberUUID, TierID: tierUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load member")
		return
	}
	var in modelTierMemberInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	params, runtimeID, err := normalizeTierMemberInput(in, &existing, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if runtimeID != nil && !h.modelTierRuntimeInWorkspace(r, wsUUID, *runtimeID) {
		writeError(w, http.StatusBadRequest, "runtime is not in this workspace")
		return
	}
	updated, err := h.Queries.UpdateModelTierMember(r.Context(), db.UpdateModelTierMemberParams{
		ID:            memberUUID,
		Position:      params.Position,
		Provider:      params.Provider,
		Model:         params.Model,
		ThinkingLevel: params.ThinkingLevel,
		RuntimeID:     params.RuntimeID,
		ProviderSlots: params.ProviderSlots,
		CooldownUntil: params.CooldownUntil,
		Enabled:       params.Enabled,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update tier member")
		return
	}
	h.syncTierLegacyFromMembers(r, tier)
	writeJSON(w, http.StatusOK, modelTierMemberToResponse(updated))
}

func (h *Handler) DeleteModelTierMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tierUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "tierId")), "tier id")
	if !ok {
		return
	}
	memberUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "memberId")), "member id")
	if !ok {
		return
	}
	tier, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{ID: tierUUID, WorkspaceID: wsUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
		return
	}
	if _, err := h.Queries.GetModelTierMemberInTier(r.Context(), db.GetModelTierMemberInTierParams{ID: memberUUID, TierID: tierUUID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "member not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load member")
		return
	}
	if err := h.Queries.DeleteModelTierMember(r.Context(), memberUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete tier member")
		return
	}
	h.syncTierLegacyFromMembers(r, tier)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ReorderModelTierMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	tierUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "tierId")), "tier id")
	if !ok {
		return
	}
	tier, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{ID: tierUUID, WorkspaceID: wsUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 200 {
		writeError(w, http.StatusBadRequest, "ids must list 1 to 200 members")
		return
	}
	seen := map[string]struct{}{}
	uuids := make([]pgtype.UUID, 0, len(req.IDs))
	for _, s := range req.IDs {
		s = strings.TrimSpace(s)
		if _, dup := seen[s]; dup {
			writeError(w, http.StatusBadRequest, "ids must not repeat a member")
			return
		}
		seen[s] = struct{}{}
		u, ok := parseUUIDOrBadRequest(w, s, "member id")
		if !ok {
			return
		}
		uuids = append(uuids, u)
	}
	if err := h.Queries.ReorderModelTierMembers(r.Context(), db.ReorderModelTierMembersParams{Ids: uuids, TierID: tierUUID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reorder members")
		return
	}
	h.syncTierLegacyFromMembers(r, tier)
	members, err := h.Queries.ListModelTierMembers(r.Context(), tierUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tier members")
		return
	}
	out := make([]ModelTierMemberResponse, 0, len(members))
	for _, m := range members {
		out = append(out, modelTierMemberToResponse(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}
