package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type issueDecisionRecordResponse struct {
	ID               string  `json:"id"`
	IssueID          string  `json:"issue_id"`
	CommentID        string  `json:"comment_id"`
	Problem          string  `json:"problem"`
	ProblemCommentID *string `json:"problem_comment_id"`
	Decided          string  `json:"decided"`
	Rationale        string  `json:"rationale"`
	SupersedesID     *string `json:"supersedes_id"`
	SupersededByID   *string `json:"superseded_by_id"`
	SourceKind       string  `json:"source_kind"`
	ActorID          string  `json:"actor_id"`
	CreatedAt        string  `json:"created_at"`
	Scope            string  `json:"scope"`
	ProjectID        *string `json:"project_id"`
}

type createIssueDecisionRecordRequest struct {
	Problem          string  `json:"problem"`
	Decided          string  `json:"decided"`
	CommentID        string  `json:"comment_id"`
	ProblemCommentID *string `json:"problem_comment_id"`
	SupersedesID     *string `json:"supersedes_id"`
	Scope            string  `json:"scope"`
}

// CreateIssueDecisionRecord lets an agent persist a decision only when it cites
// the comment that records the human resolution: a member comment, or the
// system mirror an answered ask card posts to the thread (M-500, M-609).
// The mirror is quotable but not forgeable — only the answer path writes it —
// so the record attributes the decision to the card's answerer, not the agent.
func (h *Handler) CreateIssueDecisionRecord(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req createIssueDecisionRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Problem = strings.TrimSpace(req.Problem)
	req.Decided = strings.TrimSpace(req.Decided)
	if req.Problem == "" || req.Decided == "" {
		writeError(w, http.StatusBadRequest, "problem and decided are required")
		return
	}
	if req.Scope == "" {
		req.Scope = "issue"
	}
	if req.Scope != "issue" && req.Scope != "project" {
		writeError(w, http.StatusBadRequest, "scope must be issue or project")
		return
	}
	projectID := pgtype.UUID{}
	if req.Scope == "project" {
		if !issue.ProjectID.Valid {
			writeError(w, http.StatusBadRequest, "project scope requires an issue in a project")
			return
		}
		projectID = issue.ProjectID
	}
	commentID, ok := parseUUIDOrBadRequest(w, req.CommentID, "comment_id")
	if !ok {
		return
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{ID: commentID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && comment.IssueID != issue.ID) {
		writeError(w, http.StatusBadRequest, "comment_id must reference a human comment on this issue")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to validate decision comment")
		return
	}
	actorID := comment.AuthorID
	if comment.AuthorType == "system" {
		card, cardErr := askAnswerCardForMirrorComment(r.Context(), h.Queries, issue.WorkspaceID, issue.ID, comment.Content)
		if cardErr != nil {
			writeError(w, http.StatusBadRequest, "comment_id must reference a human comment on this issue")
			return
		}
		if !card.AnsweredBy.Valid {
			writeError(w, http.StatusInternalServerError, "failed to validate decision comment")
			return
		}
		actorID = card.AnsweredBy
	} else if comment.AuthorType != "member" {
		writeError(w, http.StatusBadRequest, "comment_id must reference a human comment on this issue")
		return
	}
	problemCommentID := pgtype.UUID{}
	if req.ProblemCommentID != nil {
		problemCommentID, ok = parseUUIDOrBadRequest(w, *req.ProblemCommentID, "problem_comment_id")
		if !ok {
			return
		}
		problemComment, problemErr := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{ID: problemCommentID, WorkspaceID: issue.WorkspaceID})
		if errors.Is(problemErr, pgx.ErrNoRows) || (problemErr == nil && problemComment.IssueID != issue.ID) {
			writeError(w, http.StatusBadRequest, "problem_comment_id must reference a comment on this issue")
			return
		}
		if problemErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to validate problem comment")
			return
		}
	}
	if existing, lookupErr := h.Queries.GetIssueDecisionRecordByCommentAndDecided(r.Context(), db.GetIssueDecisionRecordByCommentAndDecidedParams{CommentID: commentID, Decided: req.Decided}); lookupErr == nil {
		writeJSON(w, http.StatusOK, decisionRecordResponse(existing.ID, existing.IssueID, existing.CommentID, existing.Problem, existing.ProblemCommentID, existing.Decided, existing.Rationale, existing.SupersedesID, pgtype.UUID{}, existing.SourceKind, existing.ActorID, existing.CreatedAt, existing.Scope, existing.ProjectID))
		return
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check existing issue decision")
		return
	}
	supersedesID := pgtype.UUID{}
	if req.SupersedesID != nil {
		supersedesID, ok = parseUUIDOrBadRequest(w, *req.SupersedesID, "supersedes_id")
		if !ok {
			return
		}
		valid, existsErr := h.Queries.IssueDecisionRecordExists(r.Context(), db.IssueDecisionRecordExistsParams{ID: supersedesID, WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
		if existsErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to validate superseded decision")
			return
		}
		if !valid {
			writeError(w, http.StatusBadRequest, "supersedes_id is not a current decision on this issue")
			return
		}
	}
	params := db.CreateIssueDecisionRecordParams{
		ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, IssueID: issue.ID,
		CommentID: commentID, Problem: req.Problem, ProblemCommentID: problemCommentID,
		Decided: req.Decided, Rationale: "", SupersedesID: supersedesID,
		SourceKind: "agent_recorded", ActorID: actorID,
		Scope: req.Scope, ProjectID: projectID,
	}
	if err := h.Queries.CreateIssueDecisionRecord(r.Context(), params); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "issue_decision_record_supersedes_idx" {
			writeError(w, http.StatusConflict, "decision was already superseded")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record issue decision")
		return
	}
	record, err := h.Queries.GetIssueDecisionRecordByCommentAndDecided(r.Context(), db.GetIssueDecisionRecordByCommentAndDecidedParams{CommentID: commentID, Decided: req.Decided})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue decision")
		return
	}
	status := http.StatusCreated
	if record.ID != params.ID {
		status = http.StatusOK
	}
	writeJSON(w, status, decisionRecordResponse(record.ID, record.IssueID, record.CommentID, record.Problem, record.ProblemCommentID, record.Decided, record.Rationale, record.SupersedesID, pgtype.UUID{}, record.SourceKind, record.ActorID, record.CreatedAt, record.Scope, record.ProjectID))
}

func (h *Handler) ListIssueDecisionRecords(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
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
				row.ID, row.IssueID, row.CommentID, row.Problem, row.ProblemCommentID, row.Decided, row.Rationale,
				row.SupersedesID, row.SupersededByID, row.SourceKind, row.ActorID, row.CreatedAt, row.Scope, row.ProjectID,
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
			row.ID, row.IssueID, row.CommentID, row.Problem, row.ProblemCommentID, row.Decided, row.Rationale,
			row.SupersedesID, row.SupersededByID, row.SourceKind, row.ActorID, row.CreatedAt, row.Scope, row.ProjectID,
		))
	}
	writeJSON(w, http.StatusOK, response)
}

func decisionRecordResponse(
	id, issueID, commentID pgtype.UUID,
	problem string, problemCommentID pgtype.UUID, decided, rationale string,
	supersedesID, supersededByID pgtype.UUID,
	sourceKind string,
	actorID pgtype.UUID,
	createdAt pgtype.Timestamptz,
	scope string, projectID pgtype.UUID,
) issueDecisionRecordResponse {
	item := issueDecisionRecordResponse{
		ID: uuidToString(id), IssueID: uuidToString(issueID),
		CommentID: uuidToString(commentID), Problem: problem, Decided: decided,
		Rationale: rationale, SourceKind: sourceKind,
		ActorID: uuidToString(actorID), CreatedAt: createdAt.Time.UTC().Format(time.RFC3339Nano), Scope: scope,
	}
	if problemCommentID.Valid {
		value := uuidToString(problemCommentID)
		item.ProblemCommentID = &value
	}
	if projectID.Valid {
		value := uuidToString(projectID)
		item.ProjectID = &value
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

// askAnswerMirrorPrefix is the exact lead of the system comment AnswerQuestionCard
// posts to the thread (mirrorQuestionAnswerToThread in internal/service/askplan.go).
const askAnswerMirrorPrefix = " answered this question card ("

// askAnswerCardForMirrorComment resolves a decision-source system comment to the
// answered ask card whose mirror it is. The comment must carry the card id in
// the mirror shape and the card must be answered on this issue in this
// workspace — so quoting the mirror proves a human answered, while any other
// system comment (or a card on another issue) stays rejected.
func askAnswerCardForMirrorComment(ctx context.Context, q *db.Queries, workspaceID, issueID pgtype.UUID, content string) (db.AgentQuestionCard, error) {
	idx := strings.Index(content, askAnswerMirrorPrefix)
	if idx < 0 {
		return db.AgentQuestionCard{}, pgx.ErrNoRows
	}
	rest := content[idx+len(askAnswerMirrorPrefix):]
	end := strings.Index(rest, ")")
	if end <= 0 {
		return db.AgentQuestionCard{}, pgx.ErrNoRows
	}
	cardID, err := util.ParseUUID(strings.TrimSpace(rest[:end]))
	if err != nil {
		return db.AgentQuestionCard{}, pgx.ErrNoRows
	}
	card, err := q.GetAgentQuestionCard(ctx, cardID)
	if err != nil {
		return db.AgentQuestionCard{}, err
	}
	if card.WorkspaceID != workspaceID || card.IssueID != issueID || card.Status != "answered" {
		return db.AgentQuestionCard{}, pgx.ErrNoRows
	}
	return card, nil
}
