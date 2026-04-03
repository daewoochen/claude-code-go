package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

type MockProvider struct{}

func (MockProvider) Name() string {
	return "mock"
}

func (MockProvider) Generate(ctx context.Context, request GenerateRequest) (GenerateResponse, error) {
	_ = ctx
	last := latestMeaningfulMessage(request.Messages)
	if last == nil {
		return GenerateResponse{
			AssistantText: "No input provided.",
			StopReason:    "end_turn",
			ProviderName:  "mock",
		}, nil
	}

	if last.Kind == runtime.MessageKindToolResult && last.ToolResult != nil {
		return GenerateResponse{
			AssistantText: fmt.Sprintf("Tool %s returned: %s", last.ToolResult.Name, last.ToolResult.Content),
			StopReason:    "end_turn",
			ProviderName:  "mock",
			Usage: runtime.Usage{
				InputTokens:  32,
				OutputTokens: 18,
			},
		}, nil
	}

	text := strings.TrimSpace(last.Content)
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "use echo"):
		payload := strings.TrimSpace(strings.TrimPrefix(text, "use echo"))
		if payload == "" {
			payload = "echo from mock provider"
		}
		call := runtime.ToolCall{
			ID:              fmt.Sprintf("tool_%d", time.Now().UnixNano()),
			Name:            "echo",
			Input:           map[string]any{"message": payload},
			ReadOnly:        true,
			ConcurrencySafe: true,
		}
		if request.OnToolCall != nil {
			if err := request.OnToolCall(call); err != nil {
				return GenerateResponse{}, err
			}
		}
		return GenerateResponse{
			ToolCalls:    []runtime.ToolCall{call},
			StopReason:   "tool_use",
			ProviderName: "mock",
			Usage: runtime.Usage{
				InputTokens:  40,
				OutputTokens: 4,
			},
		}, nil
	case strings.HasPrefix(lower, "read "):
		target := strings.TrimSpace(text[5:])
		call := runtime.ToolCall{
			ID:              fmt.Sprintf("tool_%d", time.Now().UnixNano()),
			Name:            "read_file",
			Input:           map[string]any{"path": target},
			ReadOnly:        true,
			ConcurrencySafe: true,
		}
		if request.OnToolCall != nil {
			if err := request.OnToolCall(call); err != nil {
				return GenerateResponse{}, err
			}
		}
		return GenerateResponse{
			ToolCalls:    []runtime.ToolCall{call},
			StopReason:   "tool_use",
			ProviderName: "mock",
		}, nil
	case strings.HasPrefix(lower, "run "):
		command := strings.TrimSpace(text[4:])
		call := runtime.ToolCall{
			ID:              fmt.Sprintf("tool_%d", time.Now().UnixNano()),
			Name:            "bash",
			Input:           map[string]any{"command": command},
			ReadOnly:        false,
			ConcurrencySafe: false,
		}
		if request.OnToolCall != nil {
			if err := request.OnToolCall(call); err != nil {
				return GenerateResponse{}, err
			}
		}
		return GenerateResponse{
			ToolCalls:    []runtime.ToolCall{call},
			StopReason:   "tool_use",
			ProviderName: "mock",
		}, nil
	default:
		return GenerateResponse{
			AssistantText: "mock: " + text,
			StopReason:    "end_turn",
			ProviderName:  "mock",
			Usage: runtime.Usage{
				InputTokens:  24,
				OutputTokens: 12,
			},
		}, nil
	}
}

func latestMeaningfulMessage(messages []runtime.Message) *runtime.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Kind == runtime.MessageKindSystem {
			continue
		}
		return &msg
	}
	return nil
}
