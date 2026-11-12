package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Structured agent questions.
//
// A runtime tool call that needs a decision (CodeBuddy's AskUserQuestion, and
// later the same shape from claude / omp / pi) is parked here instead of
// blocking invisibly inside the CLI. The daemon writes the questions with its
// task token, polls this resource while the tool call is held, and answers the
// tool call once every row of the group is settled.
//
// Two callers, two contracts:
//   - daemon: POST/GET/expire under /api/daemon/tasks/{taskId}/questions,
//     scoped to the task it owns.
//   - member or agent: list under /api/issues/{id}/questions and answer with
//     POST /api/questions/{id}/answer. The answer records WHO answered, which
//     is what makes an agent answering on the owner's behalf attributable.

// questionTTL bounds how long a tool call may stay parked. It is deliberately
// shorter than the CLI's own ask timeout (10 minutes in CodeBuddy) so the
// backend — not the provider — decides the run's fate, and so a forgotten
// question cannot hold a runtime slot for the full provider budget.
var questionTTL = 5 * time.Minute

const (
	questionStatusOpen     = "open"
	questionStatusAnswered = "answered"
	questionStatusExpired  = "expired"
)

// QuestionOption is one choice the asker offered.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionResponse is the wire shape for both surfaces. It carries no
// task-internal fields beyond ids the reader already has.
type QuestionResponse struct {
	ID             string           `json:"id"`
	GroupID        string           `json:"group_id"`
	IssueID        string           `json:"issue_id,omitempty"`
	TaskID         string           `json:"task_id,omitempty"`
	ToolCallID     string           `json:"tool_call_id,omitempty"`
	AskedByAgentID string           `json:"asked_by_agent_id,omitempty"`
	AddresseeType  string           `json:"addressee_type,omitempty"`
	AddresseeID    string           `json:"addressee_id,omitempty"`
	Header         string           `json:"header"`
	Prompt         string           `json:"prompt"`
	Options        []QuestionOption `json:"options"`
	MultiSelect    bool             `json:"multi_select"`
	Status         string           `json:"status"`
	Answer         string           `json:"answer,omitempty"`
	AnsweredByType string           `json:"answered_by_type,omitempty"`
	AnsweredByID   string           `json:"answered_by_id,omitempty"`
	AnsweredAt     *time.Time       `json:"answered_at,omitempty"`
	ExpiresAt      time.Time        `json:"expires_at"`
	CreatedAt      time.Time        `json:"created_at"`
}

func questionToResponse(q db.Question) QuestionResponse {
	options := make([]QuestionOption, 0, 4)
	if len(q.Options) > 0 {
		if err := json.Unmarshal(q.Options, &options); err != nil {
			options = []QuestionOption{}
		}
	}
	out := QuestionResponse{
		ID:             uuidToString(q.ID),
		GroupID:        uuidToString(q.GroupID),
		IssueID:        uuidToString(q.IssueID),
		TaskID:         uuidToString(q.TaskID),
		ToolCallID:     q.ToolCallID,
		AskedByAgentID: uuidToString(q.AskedByAgentID),
		AddresseeType:  q.AddresseeType.String,
		AddresseeID:    uuidToString(q.AddresseeID),
		Header:         q.Header,
		Prompt:         q.Prompt,
		Options:        options,
		MultiSelect:    q.MultiSelect,
		Status:         q.Status,
		Answer:         q.Answer.String,
		AnsweredByType: q.AnsweredByType.String,
		AnsweredByID:   uuidToString(q.AnsweredByID),
		ExpiresAt:      q.ExpiresAt.Time,
		CreatedAt:      q.CreatedAt.Time,
	}
	if q.AnsweredAt.Valid {
		at := q.AnsweredAt.Time
		out.AnsweredAt = &at
	}
	return out
}

// ── Daemon surface ─────────────────────────────────────────────────────────

type createQuestionItem struct {
	Header      string           `json:"header"`
	Prompt      string           `json:"prompt"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select"`
}

type createQuestionsRequest struct {
	ToolCallID string               `json:"tool_call_id"`
	Questions  []createQuestionItem `json:"questions"`
}

type createQuestionsResponse struct {
	GroupID   string             `json:"group_id"`
	ExpiresAt time.Time          `json:"expires_at"`
	Questions []QuestionResponse `json:"questions"`
}

// maxQuestionsPerAsk mirrors the provider's own limit on one AskUserQuestion
// call. Anything larger is a malformed tool input, not a legitimate ask.
const maxQuestionsPerAsk = 4

// CreateTaskQuestions records the questions a held tool call is waiting on.
func (h *Handler) CreateTaskQuestions(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, wsID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}

	var req createQuestionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Questions) == 0 || len(req.Questions) > maxQuestionsPerAsk {
		writeError(w, http.StatusBadRequest, "questions must contain 1-4 entries")
		return
	}

	wsUUID, err := util.ParseUUID(wsID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	groupID, err := uuid.NewV7()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create question")
		return
	}
	groupUUID := pgtype.UUID{Bytes: [16]byte(groupID), Valid: true}
	expiresAt := time.Now().UTC().Add(questionTTL)

	// Addressee: the person who owns the issue, if there is one. An issue
	// assigned to an agent still routes to the human who created it — the
	// point of asking is to reach someone who can decide, and a peer agent is
	// only a fallback when no member is attached to the work at all.
	addresseeType, addresseeID := h.resolveQuestionAddressee(r, task)

	// One transaction for the whole ask: a group with a missing row would be
	// a group the daemon never sees completed, and the rows that did land
	// would show up on the issue as answerable for a tool call that already
	// gave up.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create question")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	out := make([]QuestionResponse, 0, len(req.Questions))
	for i, item := range req.Questions {
		prompt := strings.TrimSpace(item.Prompt)
		if prompt == "" {
			writeError(w, http.StatusBadRequest, "question prompt is required")
			return
		}
		optionsJSON, err := json.Marshal(normalizeQuestionOptions(item.Options))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode options")
			return
		}
		row, err := qtx.CreateQuestion(r.Context(), db.CreateQuestionParams{
			WorkspaceID:    wsUUID,
			IssueID:        task.IssueID,
			TaskID:         task.ID,
			GroupID:        groupUUID,
			Position:       int32(i),
			ToolCallID:     req.ToolCallID,
			AskedByAgentID: task.AgentID,
			AddresseeType:  addresseeType,
			AddresseeID:    addresseeID,
			Header:         strings.TrimSpace(item.Header),
			Prompt:         prompt,
			Options:        optionsJSON,
			MultiSelect:    item.MultiSelect,
			ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
		})
		if err != nil {
			slog.Error("create question failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to create question")
			return
		}
		out = append(out, questionToResponse(row))
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Error("commit questions failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create question")
		return
	}
	// Published only after the commit: an event for a rolled-back row would
	// make the issue card appear for a question that does not exist.
	for _, resp := range out {
		h.publishQuestionEvent(protocol.EventQuestionCreated, wsID, resp)
	}

	writeJSON(w, http.StatusCreated, createQuestionsResponse{
		GroupID:   uuidToString(groupUUID),
		ExpiresAt: expiresAt,
		Questions: out,
	})
}

// GetTaskQuestionGroup reports whether a held tool call has been answered.
// Reading also expires the group once its deadline has passed, so a daemon
// that keeps polling after the deadline is told to stop rather than left to
// discover it by timing out.
func (h *Handler) GetTaskQuestionGroup(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	_, wsID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}
	groupID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "groupId"), "group_id")
	if !ok {
		return
	}

	if err := h.Queries.ExpireQuestionGroup(r.Context(), db.ExpireQuestionGroupParams{
		GroupID: groupID,
		Force:   false,
	}); err != nil {
		slog.Warn("expire stale questions failed", append(logger.RequestAttrs(r), "group_id", uuidToString(groupID), "error", err)...)
	}

	rows, err := h.Queries.GetQuestionGroup(r.Context(), groupID)
	if err != nil || len(rows) == 0 {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}
	// A group id is not a secret, but a daemon may only read its own task's
	// questions — the access check above authorizes the task, this one the
	// group.
	if uuidToString(rows[0].TaskID) != taskID {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}

	status := questionStatusOpen
	answered := 0
	expired := 0
	for _, row := range rows {
		switch row.Status {
		case questionStatusAnswered:
			answered++
		case questionStatusExpired:
			expired++
		}
	}
	switch {
	case expired > 0:
		status = questionStatusExpired
	case answered == len(rows):
		status = questionStatusAnswered
	}

	out := make([]QuestionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, questionToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_id":     uuidToString(groupID),
		"status":       status,
		"expires_at":   rows[0].ExpiresAt.Time,
		"questions":    out,
		"workspace_id": wsID,
	})
}

// ExpireTaskQuestionGroup is the daemon giving up: the task was cancelled, the
// run ended, or the daemon is about to restart. Force skips the deadline check
// because the reason for expiring is no longer time.
func (h *Handler) ExpireTaskQuestionGroup(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	_, wsID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}
	groupID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "groupId"), "group_id")
	if !ok {
		return
	}

	rows, err := h.Queries.GetQuestionGroup(r.Context(), groupID)
	if err != nil || len(rows) == 0 || uuidToString(rows[0].TaskID) != taskID {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}
	if err := h.Queries.ExpireQuestionGroup(r.Context(), db.ExpireQuestionGroupParams{
		GroupID: groupID,
		Force:   true,
	}); err != nil {
		slog.Error("expire question group failed", append(logger.RequestAttrs(r), "group_id", uuidToString(groupID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to expire question")
		return
	}
	h.publishQuestionEvent(protocol.EventQuestionExpired, wsID, questionToResponse(rows[0]))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// resolveQuestionAddressee picks the person a question is aimed at: the issue
// assignee when it is a member, otherwise the issue creator when it is one,
// otherwise nobody (anyone in the workspace may answer).
func (h *Handler) resolveQuestionAddressee(r *http.Request, task db.AgentTaskQueue) (pgtype.Text, pgtype.UUID) {
	if !task.IssueID.Valid {
		return pgtype.Text{}, pgtype.UUID{}
	}
	issue, err := h.Queries.GetIssue(r.Context(), task.IssueID)
	if err != nil {
		return pgtype.Text{}, pgtype.UUID{}
	}
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "member" && issue.AssigneeID.Valid {
		return issue.AssigneeType, issue.AssigneeID
	}
	if issue.CreatorType == "member" && issue.CreatorID.Valid {
		return pgtype.Text{String: "member", Valid: true}, issue.CreatorID
	}
	return pgtype.Text{}, pgtype.UUID{}
}

// ── Member / agent surface ─────────────────────────────────────────────────

// ListIssueQuestions lists the questions an issue's runs have asked. Open
// questions come first in practice because they are the only actionable ones;
// the default is every status so the issue shows what was asked and answered.
func (h *Handler) ListIssueQuestions(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != questionStatusOpen && status != questionStatusAnswered && status != questionStatusExpired {
		writeError(w, http.StatusBadRequest, "status must be open, answered or expired")
		return
	}
	limit := int32(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = int32(parsed)
	}

	if err := h.Queries.ExpireStaleQuestionsForIssue(r.Context(), issue.ID); err != nil {
		slog.Warn("expire stale issue questions failed", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "error", err)...)
	}

	rows, err := h.Queries.ListIssueQuestions(r.Context(), db.ListIssueQuestionsParams{
		IssueID: issue.ID,
		Status:  status,
		Limit:   limit,
	})
	if err != nil {
		slog.Error("list issue questions failed", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list questions")
		return
	}
	out := make([]QuestionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, questionToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"questions": out})
}

type answerQuestionRequest struct {
	// Answer is the chosen option label, the comma-joined labels of a
	// multi-select question, or free text typed under "Other". One field on
	// purpose: the provider treats all three as the same string.
	Answer string `json:"answer"`
}

// AnswerQuestion settles one question. It is the only write either surface
// shares, so attribution lives here: a member is identified by the session,
// an agent by its task-bound token (resolveActor refuses a forged X-Agent-ID).
func (h *Handler) AnswerQuestion(w http.ResponseWriter, r *http.Request) {
	questionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "question id")
	if !ok {
		return
	}
	row, err := h.Queries.GetQuestion(r.Context(), questionID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "question not found")
			return
		}
		slog.Error("get question failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load question")
		return
	}

	workspaceID := uuidToString(row.WorkspaceID)
	if h.resolveWorkspaceID(r) != workspaceID {
		writeError(w, http.StatusNotFound, "question not found")
		return
	}

	var req answerQuestionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	answer := strings.TrimSpace(req.Answer)
	if answer == "" {
		writeError(w, http.StatusBadRequest, "answer is required")
		return
	}
	if len(answer) > 4000 {
		writeError(w, http.StatusBadRequest, "answer is too long")
		return
	}

	if row.Status != questionStatusOpen {
		writeError(w, http.StatusConflict, "question is already "+row.Status)
		return
	}
	if row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(time.Now()) {
		if err := h.Queries.ExpireQuestionGroup(r.Context(), db.ExpireQuestionGroupParams{
			GroupID: row.GroupID,
			Force:   false,
		}); err != nil {
			slog.Warn("expire question failed", append(logger.RequestAttrs(r), "error", err)...)
		}
		writeError(w, http.StatusConflict, "question has expired")
		return
	}

	// Store the answer under the label the question actually offered when the
	// caller typed a near miss; free text (the provider's "Other" field) is
	// stored exactly as given.
	answer = canonicalQuestionAnswer(row, answer)

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	actorUUID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "actor not authenticated")
		return
	}
	updated, err := h.Queries.AnswerQuestion(r.Context(), db.AnswerQuestionParams{
		ID:             questionID,
		Answer:         answer,
		AnsweredByType: actorType,
		AnsweredByID:   actorUUID,
	})
	if err != nil {
		if isNotFound(err) {
			// Lost the race to another answerer (or the deadline landed
			// between the read and the write).
			writeError(w, http.StatusConflict, "question is no longer open")
			return
		}
		slog.Error("answer question failed", append(logger.RequestAttrs(r), "question_id", uuidToString(questionID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to answer question")
		return
	}

	resp := questionToResponse(updated)
	h.publishQuestionEvent(protocol.EventQuestionAnswered, workspaceID, resp)
	writeJSON(w, http.StatusOK, resp)
}

// canonicalQuestionAnswer maps an answer that matches an offered label
// case-insensitively back onto that label, so a hand-typed "postgres" reaches
// the model as "Postgres". Anything that matches no label is kept verbatim:
// every provider question carries an implicit "Other" field, so free text is a
// first-class answer and rejecting it would block a legitimate one.
func canonicalQuestionAnswer(q db.Question, answer string) string {
	if len(q.Options) == 0 {
		return answer
	}
	var options []QuestionOption
	if err := json.Unmarshal(q.Options, &options); err != nil || len(options) == 0 {
		return answer
	}
	labels := make(map[string]string, len(options))
	for _, opt := range options {
		labels[strings.ToLower(opt.Label)] = opt.Label
	}
	if !q.MultiSelect {
		if label, ok := labels[strings.ToLower(answer)]; ok {
			return label
		}
		return answer
	}
	// Multi-select answers arrive as the selected labels joined with ", ".
	parts := strings.Split(answer, ",")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if label, ok := labels[strings.ToLower(part)]; ok {
			part = label
		}
		parts[i] = part
	}
	return strings.Join(parts, ", ")
}

// normalizeQuestionOptions trims and drops blank labels: the model sometimes
// emits an empty option, and an empty label would render as an unclickable
// button on both surfaces.
func normalizeQuestionOptions(in []QuestionOption) []QuestionOption {
	out := make([]QuestionOption, 0, len(in))
	for _, opt := range in {
		label := strings.TrimSpace(opt.Label)
		if label == "" {
			continue
		}
		out = append(out, QuestionOption{Label: label, Description: strings.TrimSpace(opt.Description)})
	}
	return out
}

func (h *Handler) publishQuestionEvent(eventType, workspaceID string, q QuestionResponse) {
	issueID := q.IssueID
	if issueID == "" {
		return
	}
	h.publish(eventType, workspaceID, q.AnsweredByType, q.AnsweredByID, map[string]any{
		"question": q,
		"issue_id": issueID,
	})
}

// expireQuestionsForTask closes any question a terminal task still holds open.
// The daemon expires its own group when it stops waiting, but a daemon that
// died, was upgraded mid-run, or simply never got the chance leaves the row
// open — and an open question is what the issue card renders as answerable.
// This is the server-side backstop; it publishes nothing because the terminal
// task event already drives the client's re-read.
func (h *Handler) expireQuestionsForTask(ctx context.Context, taskID pgtype.UUID) {
	if err := h.Queries.ExpireQuestionsForTask(ctx, taskID); err != nil {
		slog.Warn("expire task questions failed", "task_id", uuidToString(taskID), "error", err)
	}
}
