package handler

// Memory compare marks and the workspace aggregate behind the panel (M-616).
//
// A host running MEMORY_SHADOW records a second memory backend's block beside
// the injected one; this file is where a person or an agent says which of the
// two was more useful for that run, and where the workspace totals are read
// back. Votes are per run, not per click: re-voting replaces the row.
//
// The numbers a vote records are read from the run row by this server, never
// sent by the caller. A vote is a judgement about two blocks that the run
// already measured, so letting a client restate their sizes would let it move
// the workspace medians.

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	memoryMarkMoct         = "moct"
	memoryMarkPowerContext = "powercontext"
	memoryMarkNeither      = "neither"
)

// memoryPartNumbers is what a run's memory part measured: the injected block's
// size and cost, and the same for the shadow block when one was read.
type memoryPartNumbers struct {
	backend         string
	bytes           int
	latencyMS       int64
	shadowBackend   string
	shadowBytes     int
	shadowLatencyMS int64
	hasShadow       bool
}

// memoryNumbersOf decodes the memory part. A run with no report, no memory part
// or a part from a daemon that predates the measurement reads as zeroes with no
// shadow, which is exactly the state that cannot be voted on.
func memoryNumbersOf(part json.RawMessage) memoryPartNumbers {
	var decoded struct {
		Backend   string `json:"backend"`
		Bytes     int    `json:"bytes"`
		LatencyMS int64  `json:"latency_ms"`
		Shadow    *struct {
			Backend   string `json:"backend"`
			Bytes     int    `json:"bytes"`
			LatencyMS int64  `json:"latency_ms"`
		} `json:"shadow"`
	}
	if len(part) == 0 || json.Unmarshal(part, &decoded) != nil {
		return memoryPartNumbers{}
	}
	numbers := memoryPartNumbers{
		backend:   decoded.Backend,
		bytes:     decoded.Bytes,
		latencyMS: decoded.LatencyMS,
	}
	if decoded.Shadow != nil {
		numbers.hasShadow = true
		numbers.shadowBackend = decoded.Shadow.Backend
		numbers.shadowBytes = decoded.Shadow.Bytes
		numbers.shadowLatencyMS = decoded.Shadow.LatencyMS
	}
	return numbers
}

// MemoryMarkResponse is one run's vote. The ids of who voted are recorded in
// the row but not returned: the panel shows whether a person or an agent judged
// the run, which is the part a reader needs to weigh the vote.
type MemoryMarkResponse struct {
	TaskID           string `json:"task_id"`
	Choice           string `json:"choice"`
	MarkedByType     string `json:"marked_by_type"`
	PrimaryBytes     int32  `json:"primary_bytes"`
	PrimaryLatencyMS int32  `json:"primary_latency_ms"`
	ShadowBytes      int32  `json:"shadow_bytes"`
	ShadowLatencyMS  int32  `json:"shadow_latency_ms"`
	MarkedAt         string `json:"marked_at"`
}

func memoryMarkResponse(mark db.TaskMemoryMark) *MemoryMarkResponse {
	return &MemoryMarkResponse{
		TaskID:           uuidToString(mark.TaskID),
		Choice:           mark.Choice,
		MarkedByType:     mark.MarkedByType,
		PrimaryBytes:     mark.PrimaryBytes,
		PrimaryLatencyMS: mark.PrimaryLatencyMs,
		ShadowBytes:      mark.ShadowBytes,
		ShadowLatencyMS:  mark.ShadowLatencyMs,
		MarkedAt:         timestampToString(mark.UpdatedAt),
	}
}

// SetTaskMemoryMark records which memory block a run's reader found more
// useful. Members and agents may both vote: the point of the two-week
// comparison is that the run's own agent can say which block it used.
func (h *Handler) SetTaskMemoryMark(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          parseUUID(chi.URLParam(r, "taskId")),
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || task.IssueID != issue.ID {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	var req struct {
		Choice string `json:"choice"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	choice := strings.TrimSpace(req.Choice)
	switch choice {
	case memoryMarkMoct, memoryMarkPowerContext, memoryMarkNeither:
	default:
		writeError(w, http.StatusBadRequest, "choice must be moct, powercontext or neither")
		return
	}

	numbers := memoryNumbersOf(memoryReportPart(task.Result))
	// Only a run that actually read a second block can be compared. Letting a
	// single-block run into the aggregate would count a guess as evidence.
	if !numbers.hasShadow {
		writeError(w, http.StatusConflict, "this run has no shadow memory block to compare")
		return
	}

	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	mark, err := h.Queries.UpsertTaskMemoryMark(r.Context(), db.UpsertTaskMemoryMarkParams{
		TaskID:           task.ID,
		WorkspaceID:      issue.WorkspaceID,
		Choice:           choice,
		MarkedByType:     actorType,
		MarkedByID:       parseUUID(actorID),
		PrimaryBytes:     int32(numbers.bytes),
		PrimaryLatencyMs: int32(numbers.latencyMS),
		ShadowBytes:      int32(numbers.shadowBytes),
		ShadowLatencyMs:  int32(numbers.shadowLatencyMS),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save the mark")
		return
	}
	writeJSON(w, http.StatusOK, memoryMarkResponse(mark))
}

// MemoryComparisonBackend is one backend's median cost across the workspace's
// judged runs.
type MemoryComparisonBackend struct {
	Backend   string `json:"backend"`
	Bytes     int    `json:"bytes"`
	LatencyMS int    `json:"latency_ms"`
}

// MemoryComparisonResponse is the workspace aggregate: how the votes split and
// what each backend cost on the runs that were judged.
type MemoryComparisonResponse struct {
	WorkspaceID string `json:"workspace_id"`
	Marked      int64  `json:"marked"`
	Votes       struct {
		Moct         int64 `json:"moct"`
		PowerContext int64 `json:"powercontext"`
		Neither      int64 `json:"neither"`
	} `json:"votes"`
	PreferencePct struct {
		Moct         int `json:"moct"`
		PowerContext int `json:"powercontext"`
		Neither      int `json:"neither"`
	} `json:"preference_pct"`
	Backends []MemoryComparisonBackend `json:"backends"`
}

// GetWorkspaceMemoryComparison reads the aggregate for the panel. Member
// visible: it counts votes, and no vote says anything about the person behind
// it beyond "a person" or "an agent".
func (h *Handler) GetWorkspaceMemoryComparison(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	stats, err := h.Queries.GetWorkspaceMemoryMarkStats(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read the memory comparison")
		return
	}
	resp := MemoryComparisonResponse{
		WorkspaceID: uuidToString(workspaceID),
		Marked:      stats.Marked,
		Backends: []MemoryComparisonBackend{
			{
				Backend:   memoryMarkMoct,
				Bytes:     int(math.Round(stats.MedianMoctBytes)),
				LatencyMS: int(math.Round(stats.MedianMoctLatencyMs)),
			},
			{
				Backend:   memoryMarkPowerContext,
				Bytes:     int(math.Round(stats.MedianPowercontextBytes)),
				LatencyMS: int(math.Round(stats.MedianPowercontextLatencyMs)),
			},
		},
	}
	resp.Votes.Moct = stats.MoctVotes
	resp.Votes.PowerContext = stats.PowercontextVotes
	resp.Votes.Neither = stats.NeitherVotes
	if stats.Marked > 0 {
		pct := func(n int64) int { return int(math.Round(float64(n) * 100 / float64(stats.Marked))) }
		resp.PreferencePct.Moct = pct(stats.MoctVotes)
		resp.PreferencePct.PowerContext = pct(stats.PowercontextVotes)
		resp.PreferencePct.Neither = pct(stats.NeitherVotes)
	}
	writeJSON(w, http.StatusOK, resp)
}

// taskMemoryMark reads a run's vote, or nil when nobody has judged it yet. A
// failed read is also nil: the mark is one section of the run report and must
// not cost a reader the report it sits under.
func (h *Handler) taskMemoryMark(r *http.Request, taskID, workspaceID pgtype.UUID) *MemoryMarkResponse {
	mark, err := h.Queries.GetTaskMemoryMark(r.Context(), db.GetTaskMemoryMarkParams{
		TaskID:      taskID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil
	}
	return memoryMarkResponse(mark)
}
