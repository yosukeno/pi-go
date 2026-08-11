package llm

// toWireMessages flattens neutral messages into the OpenAI shape. The two shapes
// disagree in one place: a turn's tool results live in a single neutral message
// but must go out as one role:"tool" entry per call, immediately after the
// assistant message that requested them.
func toWireMessages(systemPrompt string, history []Message) []wireMessage {
	out := make([]wireMessage, 0, len(history)+1)
	if systemPrompt != "" {
		out = append(out, wireMessage{Role: "system", Content: systemPrompt})
	}

	for _, m := range history {
		var (
			text    string
			calls   []wireToolCall
			results []wireMessage
		)
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				text += b.Text
			case BlockThinking:
				// Not replayed. Chat-completions has no signed thinking block to
				// echo back, and sending the field costs input tokens for
				// reasoning the model does not reuse.
			case BlockToolUse:
				calls = append(calls, wireToolCall{
					ID:       b.ID,
					Type:     "function",
					Function: wireFunction{Name: b.Name, Arguments: string(b.Input)},
				})
			case BlockToolResult:
				results = append(results, wireMessage{
					Role:       "tool",
					ToolCallID: b.ToolUseID,
					Content:    b.Text,
				})
			}
		}

		if m.Role == RoleAssistant {
			if text != "" || len(calls) > 0 {
				out = append(out, wireMessage{Role: "assistant", Content: text, ToolCalls: calls})
			}
		} else if text != "" {
			out = append(out, wireMessage{Role: "user", Content: text})
		}
		out = append(out, results...)
	}
	return out
}

func toWireTools(tools []ToolSchema) []wireTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]wireTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, wireTool{
			Type: "function",
			Function: wireToolSchema{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}
