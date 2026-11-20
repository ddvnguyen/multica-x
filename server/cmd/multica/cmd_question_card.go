package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// multica question-card {show|answer} — the project lead's half of the ask/plan
// escalation ladder (M-332). When an ask card outlives its timeout the sweep
// escalates it once to the project lead; the lead may answer it on the Owner's
// behalf only when the approved plan or the Owner's recorded decisions cover
// the decision, or when it is reversible, and "do not answer" is a valid
// outcome. The card stays open for the Owner either way, and a lead answer is
// stored as the lead's (answered_by_type='agent'), never as the Owner's.

var questionCardCmd = &cobra.Command{
	Use:   "question-card",
	Short: "Read and answer an escalated ask card as the project lead",
	Long: `Read and answer an escalated ask/plan question card.

Only the issue's project lead agent may use these commands, only with its task
token, and only after the card was escalated (the run's task token carries it).
Answering a card on the Owner's behalf is allowed only when the decision is
covered by the approved plan or the Owner's recorded decisions, or is
reversible; otherwise leave the card for the Owner and say so in one reply.`,
}

var questionCardShowCmd = &cobra.Command{
	Use:   "show <card-id>",
	Short: "Show an escalated card's questions and options",
	Args:  exactArgs(1),
	RunE:  runQuestionCardShow,
}

var questionCardAnswerCmd = &cobra.Command{
	Use:   "answer <card-id>",
	Short: "Answer an escalated card on the Owner's behalf",
	Long: `Answer an escalated card on the Owner's behalf.

Pass one --option per question with the option label the card offered (repeat
it for a multi-select question), or --text for a free-text answer to the
question that has no option. When the same label exists on more than one
question, prefix it with the question number: --option 2:<label>.`,
	Args: exactArgs(1),
	RunE: runQuestionCardAnswer,
}

func init() {
	questionCardCmd.AddCommand(questionCardShowCmd)
	questionCardCmd.AddCommand(questionCardAnswerCmd)

	questionCardShowCmd.Flags().String("output", "table", "Output format: table or json")
	questionCardAnswerCmd.Flags().StringArray("option", nil, "Option label to answer with (repeatable)")
	questionCardAnswerCmd.Flags().String("text", "", "Free-text answer for the question with no option")
	questionCardAnswerCmd.Flags().String("output", "table", "Output format: table or json")
}

// questionCardPayload is the agent-facing read shape of an ask card
// (GET /api/question-cards/{id}); only what an answerer needs is decoded.
type questionCardPayload struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Context string `json:"context"`
	Questions []struct {
		Prompt   string `json:"prompt"`
		Multiple bool   `json:"multiple"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

type questionCardAnswer struct {
	Selected []int  `json:"selected"`
	Text     string `json:"text"`
}

func fetchQuestionCard(client *cli.APIClient, cardID string) (questionCardPayload, error) {
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var card questionCardPayload
	if err := client.GetJSON(ctx, "/api/question-cards/"+strings.TrimSpace(cardID), &card); err != nil {
		return questionCardPayload{}, fmt.Errorf("read question card: %w", err)
	}
	return card, nil
}

func runQuestionCardShow(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	card, err := fetchQuestionCard(client, args[0])
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, card)
	}
	fmt.Printf("Card %s — %s\n", card.ID, card.Status)
	if strings.TrimSpace(card.Context) != "" {
		fmt.Printf("Context: %s\n", card.Context)
	}
	for i, question := range card.Questions {
		kind := "single choice"
		if question.Multiple {
			kind = "multi-select"
		}
		fmt.Printf("\n%d. %s (%s)\n", i+1, question.Prompt, kind)
		for j, option := range question.Options {
			line := fmt.Sprintf("   [%d] %s", j+1, option.Label)
			if option.Description != "" {
				line += " — " + option.Description
			}
			fmt.Println(line)
		}
	}
	return nil
}

func runQuestionCardAnswer(cmd *cobra.Command, args []string) error {
	cardID := strings.TrimSpace(args[0])
	labels, _ := cmd.Flags().GetStringArray("option")
	text, _ := cmd.Flags().GetString("text")
	text = strings.TrimSpace(text)
	if len(labels) == 0 && text == "" {
		return fmt.Errorf("an answer is required: pass --option <label> or --text <answer>")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	card, err := fetchQuestionCard(client, cardID)
	if err != nil {
		return err
	}
	if card.Status != "open" {
		return fmt.Errorf("the card is already %s", card.Status)
	}

	answers, err := buildCardAnswers(card, labels, text)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var answered map[string]any
	if err := client.PostJSON(ctx, "/api/question-cards/"+cardID+"/lead-answer", map[string]any{"answers": answers}, &answered); err != nil {
		return fmt.Errorf("answer question card: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, answered)
	}
	cardBody, _ := answered["card"].(map[string]any)
	cli.PrintTable(os.Stdout, []string{"CARD", "STATUS", "RESUME TASK"}, [][]string{{
		strVal(cardBody, "id"),
		strVal(cardBody, "status"),
		strVal(answered, "resume_task"),
	}})
	return nil
}

// buildCardAnswers maps the lead's labels onto the card's option indices. The
// API answers by index (the UI's shape), but a lead reads labels — resolving
// them here is what makes `--option "Ship it"` enough.
func buildCardAnswers(card questionCardPayload, labels []string, text string) ([]questionCardAnswer, error) {
	answers := make([]questionCardAnswer, len(card.Questions))
	chosen := make([]bool, len(card.Questions))
	for _, raw := range labels {
		label := strings.TrimSpace(raw)
		if label == "" {
			continue
		}
		questionHint := 0
		if head, tail, found := strings.Cut(label, ":"); found {
			if n, err := strconv.Atoi(strings.TrimSpace(head)); err == nil {
				questionHint, label = n, strings.TrimSpace(tail)
			}
		}
		matches := make([][2]int, 0, 1)
		for q, question := range card.Questions {
			for o, option := range question.Options {
				if strings.EqualFold(strings.TrimSpace(option.Label), label) {
					matches = append(matches, [2]int{q, o})
				}
			}
		}
		switch {
		case len(matches) == 0:
			return nil, fmt.Errorf("no question offers the option %q", label)
		case len(matches) > 1 && questionHint == 0:
			return nil, fmt.Errorf("the option %q exists on more than one question; prefix it with the question number, e.g. --option 2:%s", label, label)
		case len(matches) > 1:
			filtered := matches[:0]
			for _, m := range matches {
				if m[0] == questionHint-1 {
					filtered = append(filtered, m)
				}
			}
			if len(filtered) == 0 {
				return nil, fmt.Errorf("question %d does not offer the option %q", questionHint, label)
			}
			matches = filtered
		}
		q, o := matches[0][0], matches[0][1]
		if !card.Questions[q].Multiple && len(answers[q].Selected) > 0 {
			return nil, fmt.Errorf("question %d takes one option", q+1)
		}
		answers[q].Selected = append(answers[q].Selected, o)
		chosen[q] = true
	}

	if text != "" {
		free := -1
		for q := range card.Questions {
			if chosen[q] {
				continue
			}
			if free >= 0 {
				return nil, fmt.Errorf("--text is ambiguous: questions %d and %d both need an answer; answer them with --option", free+1, q+1)
			}
			free = q
		}
		if free < 0 {
			return nil, fmt.Errorf("--text was given but every question already has an option")
		}
		answers[free].Text = text
		chosen[free] = true
	}

	for q := range card.Questions {
		if !chosen[q] {
			return nil, fmt.Errorf("question %d has no answer; pass --option or --text", q+1)
		}
	}
	return answers, nil
}
