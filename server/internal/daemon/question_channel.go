package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// Structured questions: the daemon's side of the ask channel.
//
// A backend that can ask the user something hands its tool call here instead
// of blocking inside the provider CLI. The question becomes rows on the server
// (visible on the issue), this loop polls until somebody answers or the group
// expires, and the backend resumes the tool call with the answer.
//
// Polling rather than a push: the daemon already polls task status on the same
// connection, and a question that nobody answers must expire on the server's
// clock whether or not this daemon is still alive.

const (
	// questionPollInterval bounds answer latency. One second is below the
	// threshold where a click feels unresponsive, and a five-minute TTL costs
	// at most 300 requests for a question nobody answers.
	questionPollInterval = time.Second

	// questionExpireTimeout bounds the best-effort expire call made when a run
	// is cancelled. It must not inherit the cancelled context it is reacting
	// to, and it must not delay the run's teardown.
	questionExpireTimeout = 5 * time.Second

	// questionCancelledReason is shown to the model when the run ended before
	// anybody answered. It must read as an instruction: the provider forwards
	// it into the tool result.
	questionCancelledReason = "the run was cancelled before anyone answered; continue with your best assumption or end the turn"
	questionExpiredReason   = "no answer arrived in time; continue with your best assumption or end the turn"
)

// QuestionOption is one offered choice on the wire.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionCreateItem is one question in a create request.
type QuestionCreateItem struct {
	Header      string           `json:"header"`
	Prompt      string           `json:"prompt"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select"`
}

// QuestionRecord is one stored question as the daemon reads it back.
type QuestionRecord struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Status string `json:"status"`
	Answer string `json:"answer,omitempty"`
}

// QuestionGroup is the state of one held tool call.
type QuestionGroup struct {
	GroupID   string           `json:"group_id"`
	Status    string           `json:"status"`
	Questions []QuestionRecord `json:"questions"`
}

// CreateTaskQuestions records the questions a held tool call is waiting on.
func (c *Client) CreateTaskQuestions(ctx context.Context, taskID, toolCallID string, questions []agent.AskQuestion) (*QuestionGroup, error) {
	items := make([]QuestionCreateItem, 0, len(questions))
	for _, q := range questions {
		options := make([]QuestionOption, 0, len(q.Options))
		for _, opt := range q.Options {
			options = append(options, QuestionOption{Label: opt.Label, Description: opt.Description})
		}
		items = append(items, QuestionCreateItem{
			Header:      q.Header,
			Prompt:      q.Prompt,
			Options:     options,
			MultiSelect: q.MultiSelect,
		})
	}
	var group QuestionGroup
	if err := c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/questions", taskID), map[string]any{
		"tool_call_id": toolCallID,
		"questions":    items,
	}, &group); err != nil {
		return nil, err
	}
	return &group, nil
}

// GetTaskQuestionGroup reads the current state of one held tool call.
func (c *Client) GetTaskQuestionGroup(ctx context.Context, taskID, groupID string) (*QuestionGroup, error) {
	var group QuestionGroup
	if err := c.getJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/questions/%s", taskID, groupID), &group); err != nil {
		return nil, err
	}
	return &group, nil
}

// ExpireTaskQuestionGroup tells the server nobody is waiting on this group any
// more, so the issue stops showing it as answerable.
func (c *Client) ExpireTaskQuestionGroup(ctx context.Context, taskID, groupID string) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/daemon/tasks/%s/questions/%s/expire", taskID, groupID), map[string]any{}, nil)
}

// daemonQuestionChannel is the Asker a backend gets for one run.
type daemonQuestionChannel struct {
	client *Client
	taskID string
	log    *slog.Logger
}

func newQuestionChannel(client *Client, taskID string, log *slog.Logger) *daemonQuestionChannel {
	if log == nil {
		log = slog.Default()
	}
	return &daemonQuestionChannel{client: client, taskID: taskID, log: log}
}

// Ask records the questions and blocks until they are answered, expire, or the
// run is cancelled.
func (q *daemonQuestionChannel) Ask(ctx context.Context, req agent.AskRequest) (agent.AskAnswer, error) {
	group, err := q.client.CreateTaskQuestions(ctx, q.taskID, req.ToolCallID, req.Questions)
	if err != nil {
		return agent.AskAnswer{}, fmt.Errorf("create questions: %w", err)
	}
	q.log.Info("question opened", "task_id", q.taskID, "group_id", group.GroupID, "questions", len(req.Questions))

	ticker := time.NewTicker(questionPollInterval)
	defer ticker.Stop()

	// The run ended while the question was open: expire it so the issue stops
	// showing an answerable card, and tell the model to carry on rather than
	// leaving the tool call to be torn down mid-flight.
	cancelled := func() (agent.AskAnswer, error) {
		q.expire(group.GroupID)
		return agent.AskAnswer{Declined: true, Reason: questionCancelledReason}, nil
	}

	for {
		select {
		case <-ctx.Done():
			return cancelled()
		case <-ticker.C:
			state, err := q.client.GetTaskQuestionGroup(ctx, q.taskID, group.GroupID)
			if err != nil {
				if ctx.Err() != nil {
					return cancelled()
				}
				// The question is still open on the server; a failed poll is
				// a transport problem, not an answer. Keep waiting.
				q.log.Warn("question poll failed", "task_id", q.taskID, "group_id", group.GroupID, "error", err)
				continue
			}
			switch state.Status {
			case "answered":
				answers := make(map[string]string, len(state.Questions))
				for _, row := range state.Questions {
					if row.Answer != "" {
						answers[row.Prompt] = row.Answer
					}
				}
				q.log.Info("question answered", "task_id", q.taskID, "group_id", group.GroupID)
				return agent.AskAnswer{Answers: answers}, nil
			case "expired":
				q.log.Info("question expired", "task_id", q.taskID, "group_id", group.GroupID)
				return agent.AskAnswer{Declined: true, Reason: questionExpiredReason}, nil
			}
		}
	}
}

// expire is best-effort: the server's TTL is the durable guard, so a failure
// here only delays the issue card's update.
func (q *daemonQuestionChannel) expire(groupID string) {
	ctx, cancel := context.WithTimeout(context.Background(), questionExpireTimeout)
	defer cancel()
	if err := q.client.ExpireTaskQuestionGroup(ctx, q.taskID, groupID); err != nil {
		q.log.Warn("expire question failed", "task_id", q.taskID, "group_id", groupID, "error", err)
	}
}
