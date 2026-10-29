// Translate turns dsh session events into the task-message rows the Multica
// web transcript renders (types text / thinking / tool_use / tool_result, see
// protocol.TaskMessagePayload and build-timeline.ts).
//
// What is dropped, deliberately:
//   - user/message and system/message frames: the Multica run row already
//     shows the task's own prompt, and the runtime-context injections dsh
//     splices into the inbox are internal to the dsh session, not agent
//     output the transcript is meant to show;
//   - assistant/attempt streaming frames (only the committed
//     assistant/message is rendered) and tool-call parts inside assistant
//     messages (the standalone tool/call event is the rendering source —
//     rendering both would duplicate every call);
//   - every other known v4 type: session bookkeeping, not transcript.

package dshsession

import "encoding/json"

// unquoteJSON normalizes dsh tool arguments: dsh stores them as a JSON string
// literal (the model's raw arguments string), so `"{\"a\":1}"` must surface
// as `{"a":1}` — the shape the transcript renders and task_message.input
// stores. Anything that is not a JSON string passes through unchanged.
func unquoteJSON(raw json.RawMessage) string {
	if len(raw) > 0 && raw[0] == '"' {
		var inner string
		if json.Unmarshal(raw, &inner) == nil {
			return inner
		}
	}
	return string(raw)
}

// Translate assigns dense 1-based Seqs in log order and carries each event's
// own timestamp (epoch milliseconds) through to CreatedAt.
func Translate(events []Event) []Entry {
	entries := make([]Entry, 0, len(events))
	for _, e := range events {
		switch e.Type {
		case "assistant/message":
			var d messageData
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			for _, part := range d.Message.Content {
				switch part.Type {
				case "text":
					if part.Text == "" {
						continue
					}
					entries = append(entries, Entry{Type: "text", Content: part.Text, CreatedAt: e.Time})
				case "reasoning":
					if part.Text == "" {
						continue
					}
					entries = append(entries, Entry{Type: "thinking", Content: part.Text, CreatedAt: e.Time})
				}
				// "tool-call" parts are skipped: the standalone tool/call
				// event renders the call once.
			}
		case "tool/call":
			var d toolCallData
			if json.Unmarshal(e.Data, &d) != nil || d.Name == "" {
				continue
			}
			entries = append(entries, Entry{
				Type:      "tool_use",
				Tool:      d.Name,
				CallID:    d.CallID,
				Input:     unquoteJSON(d.Arguments),
				CreatedAt: e.Time,
			})
		case "tool/result":
			var d toolResultData
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			output := ""
			for _, part := range d.Message.Content {
				if part.Type == "text" && part.Text != "" {
					if output != "" {
						output += "\n"
					}
					output += part.Text
				}
			}
			entries = append(entries, Entry{
				Type:      "tool_result",
				CallID:    d.Message.ToolCallID,
				Output:    output,
				IsError:   d.Message.IsError,
				CreatedAt: e.Time,
			})
		}
	}
	for i := range entries {
		entries[i].Seq = i + 1
	}
	return entries
}
