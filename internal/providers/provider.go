package providers

import (
	"context"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

type GenerateRequest struct {
	Model            string
	SystemPrompt     string
	Messages         []runtime.Message
	Tools            []runtime.ToolDescriptor
	MaxOutputTokens  int
	OnAssistantDelta func(string)
}

type GenerateResponse struct {
	AssistantText string
	ToolCalls     []runtime.ToolCall
	StopReason    string
	Usage         runtime.Usage
	ProviderName  string
	StreamedText  bool
}

type ChatModelProvider interface {
	Name() string
	Generate(ctx context.Context, request GenerateRequest) (GenerateResponse, error)
}
