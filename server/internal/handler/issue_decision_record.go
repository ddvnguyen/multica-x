package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	currentOnly := r.URL.Query().Get("current") == "true"
	var response []issueDecisionRecordResponse
	if currentOnly {
		rows, err := h.Queries.ListCurrentIssueDecisionRecords(r.Context(), db.ListCurrentIssueDecisionRecordsParams{
			WorkspaceID: issue.WorkspaceID, IssueID: issue.ID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list current issue decision records")
			return
		}
		response = make([]issueDecisionRecordResponse, 0, len(rows))
		for _, row := range rows {
			response = append(response, decisionRecordResponse(
				row.ID, row.IssueID, row.CommentID, row.Decided, row.Rationale,
				row.SupersedesID, row.SupersededByID, row.SourceKind, row.ActorID, row.CreatedAt,
			))
		}
		writeJSON(w, http.StatusOK, response)
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
	response = make([]issueDecisionRecordResponse, 0, len(rows))
	for _, row := range rows {
		response = append(response, decisionRecordResponse(
			row.ID, row.IssueID, row.CommentID, row.Decided, row.Rationale,
			row.SupersedesID, row.SupersededByID, row.SourceKind, row.ActorID, row.CreatedAt,
		))
	}
	writeJSON(w, http.StatusOK, response)
}

func decisionRecordResponse(
	id, issueID, commentID pgtype.UUID,
	decided, rationale string,
	supersedesID, supersededByID pgtype.UUID,
	sourceKind string,
	actorID pgtype.UUID,
	createdAt pgtype.Timestamptz,
) issueDecisionRecordResponse {
	item := issueDecisionRecordResponse{
		ID: uuidToString(id), IssueID: uuidToString(issueID),
		CommentID: uuidToString(commentID), Decided: decided,
		Rationale: rationale, SourceKind: sourceKind,
		ActorID: uuidToString(actorID), CreatedAt: createdAt.Time.UTC().Format(time.RFC3339Nano),
	}
	if supersedesID.Valid {
		value := uuidToString(supersedesID)
		item.SupersedesID = &value
	}
	if supersededByID.Valid {
		value := uuidToString(supersededByID)
		item.SupersededByID = &value
	}
	return item
}
