package handler

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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

// ModelTierResponse is the wire shape of one tier.
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
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
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
		CreatedAt:     timestampToString(t.CreatedAt),
		UpdatedAt:     timestampToString(t.UpdatedAt),
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
	Enabled       *bool   `json:"enabled"`
}

func (in *modelTierInput) UnmarshalJSON(data []byte) error {
	type alias struct {
		Name          *string `json:"name"`
		Idx           *int    `json:"idx"`
		RuntimeID     *string `json:"runtime_id"`
		Provider      *string `json:"provider"`
		Model         *string `json:"model"`
		ThinkingLevel *string `json:"thinking_level"`
		ServiceTier   *string `json:"service_tier"`
		Cost          any     `json:"cost"`
		Quality       *int    `json:"quality"`
		ProviderSlots *int    `json:"provider_slots"`
		Enabled       *bool   `json:"enabled"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	in.Name, in.Idx, in.RuntimeID = a.Name, a.Idx, a.RuntimeID
	in.Provider, in.Model = a.Provider, a.Model
	in.ThinkingLevel, in.ServiceTier = a.ThinkingLevel, a.ServiceTier
	in.Quality, in.ProviderSlots, in.Enabled = a.Quality, a.ProviderSlots, a.Enabled
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
	}

	name := params.Name
	if in.Name != nil || forCreate {
		n := name
		if in.Name != nil {
			n = strings.TrimSpace(*in.Name)
		}
		if n == "" {
			return params, nil, errors.New("name is required")
		}
		if len(n) > 100 {
			return params, nil, errors.New("name must be 100 characters or fewer")
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
	if in.Quality != nil {
		if *in.Quality < 0 || *in.Quality > 100 {
			return params, nil, errors.New("quality must be between 0 and 100")
		}
		params.Quality = pgtype.Int4{Int32: int32(*in.Quality), Valid: true}
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
	return params, runtimeID, nil
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

// ListModelTiers returns the workspace's tiers ordered by idx. Member-visible:
// the payload names runtimes and models only, and M-351 agent pickers need it.
func (h *Handler) ListModelTiers(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
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
		writeError(w, http.StatusInternalServerError, "failed to create tier")
		return
	}
	writeJSON(w, http.StatusCreated, modelTierToResponse(created))
}

// UpdateModelTier edits a tier (owner/admin). Omitted fields keep their value;
// an explicit empty runtime_id unbinds the runtime.
func (h *Handler) UpdateModelTier(w http.ResponseWriter, r *http.Request) {
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
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update tier")
		return
	}
	writeJSON(w, http.StatusOK, modelTierToResponse(updated))
}

// DeleteModelTier removes a tier (owner/admin).
func (h *Handler) DeleteModelTier(w http.ResponseWriter, r *http.Request) {
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
	if _, err := h.Queries.GetModelTierInWorkspace(r.Context(), db.GetModelTierInWorkspaceParams{
		ID: tierUUID, WorkspaceID: wsUUID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tier not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load tier")
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
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 100 {
		writeError(w, http.StatusBadRequest, "ids must list 1 to 100 tiers")
		return
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
