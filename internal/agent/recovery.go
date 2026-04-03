package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

const (
	maxReactiveCompactAttempts = 2
	maxOutputRecoveryAttempts  = 2
	compactKeepTailMessages    = 8
)

func isPromptTooLongError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "prompt is too long") ||
		strings.Contains(lower, "prompt too long") ||
		strings.Contains(lower, "context length") ||
		strings.Contains(lower, "too many tokens") ||
		strings.Contains(lower, "input is too long")
}

func isMaxOutputStopReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

func compactStateForRetry(state *runtime.SessionState, reason string) bool {
	if len(state.Messages) <= compactKeepTailMessages+1 {
		for i := range state.Messages {
			content := strings.TrimSpace(state.Messages[i].Content)
			if len(content) <= 256 {
				continue
			}
			state.Messages[i].Content = content[:256] + "\n[ccgo compact truncation]"
			if state.Messages[i].ToolResult != nil {
				state.Messages[i].ToolResult.Content = state.Messages[i].Content
			}
			state.ReactiveCompactCount++
			state.LastCompactionReason = reason
			return true
		}
		return false
	}

	cut := len(state.Messages) - compactKeepTailMessages
	head := append([]runtime.Message(nil), state.Messages[:cut]...)
	tail := append([]runtime.Message(nil), state.Messages[cut:]...)
	summaryLines := make([]string, 0, len(head)+2)
	summaryLines = append(summaryLines, "[ccgo compact summary]")
	summaryLines = append(summaryLines, fmt.Sprintf("Compaction reason: %s", reason))
	for _, message := range head {
		if message.Kind == runtime.MessageKindSystem {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" && message.ToolCall != nil {
			content = fmt.Sprintf("tool call: %s", message.ToolCall.Name)
		}
		if content == "" && message.ToolResult != nil {
			content = message.ToolResult.Content
		}
		if len(content) > 120 {
			content = content[:120] + "..."
		}
		summaryLines = append(summaryLines, fmt.Sprintf("- %s/%s: %s", message.Role, message.Kind, content))
	}
	summary := runtime.Message{
		ID:        generateMessageID("summary"),
		Role:      runtime.RoleAssistant,
		Kind:      runtime.MessageKindText,
		Content:   strings.Join(summaryLines, "\n"),
		CreatedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"synthetic": "true",
			"compact":   "true",
		},
	}

	state.Messages = append([]runtime.Message{summary}, tail...)
	state.ReactiveCompactCount++
	state.LastCompactionReason = reason
	return true
}

func appendContinuationMessage(state *runtime.SessionState) {
	state.Messages = append(state.Messages, runtime.Message{
		ID:        generateMessageID("meta"),
		Role:      runtime.RoleUser,
		Kind:      runtime.MessageKindText,
		Content:   "[ccgo meta] Continue exactly where you stopped. No recap. No apology.",
		CreatedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"meta": "true",
		},
	})
	state.MaxOutputRecoveryCount++
}
