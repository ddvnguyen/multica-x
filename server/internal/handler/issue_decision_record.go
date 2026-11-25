package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type issueDecisionRecordResponse struct {
	ID             string  `json:"id"`
	IssueID        string  `json:"issue_id"`
	CommentID      string  `json:"comment_id"`
	Decided        string  `json:"decided"`
	Rationale      string  `json:"rationale"`
	SupersedesID   *string `json:"supersedes_id"`
	SupersededByID *string `json:"superseded_by_id"`
	SourceKind     string  `json:"source_kind"`
	ActorID        string  `json:"actor_id"`
	CreatedAt      string  `json:"created_at"`
}

func (h *Handler) ListIssueDecisionRecords(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "issueId"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueDecisionRecords(r.Context(), db.ListIssueDecisionRecordsParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue decision records")
		return
	}
	response := make([]issueDecisionRecordResponse, 0, len(rows))
	for _, row := range rows {
		item := issueDecisionRecordResponse{
			ID: uuidToString(row.ID), IssueID: uuidToString(row.IssueID),
			CommentID: uuidToString(row.CommentID), Decided: row.Decided,
			Rationale: row.Rationale, SourceKind: row.SourceKind,
			ActorID: uuidToString(row.ActorID), CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		}
		if row.SupersedesID.Valid {
			value := uuidToString(row.SupersedesID)
			item.SupersedesID = &value
		}
		if row.SupersededByID.Valid {
			value := uuidToString(row.SupersededByID)
			item.SupersededByID = &value
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, response)
}
