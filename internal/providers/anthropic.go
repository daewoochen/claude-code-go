package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

const defaultAnthropicVersion = "2023-06-01"

type AnthropicProvider struct {
	APIKey     string
	BaseURL    string
	Version    string
	HTTPClient *http.Client
}

func (p AnthropicProvider) Name() string {
	return "anthropic"
}

func (p AnthropicProvider) Generate(ctx context.Context, request GenerateRequest) (GenerateResponse, error) {
	if strings.TrimSpace(p.APIKey) == "" {
		return GenerateResponse{}, fmt.Errorf("anthropic api key is empty")
	}
	baseURL := strings.TrimRight(p.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	version := p.Version
	if version == "" {
		version = defaultAnthropicVersion
	}
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}

	body := anthropicRequest{
		Model:     chooseModel(request.Model),
		System:    request.SystemPrompt,
		MaxTokens: request.MaxOutputTokens,
		Messages:  toAnthropicMessages(request.Messages),
		Tools:     toAnthropicTools(request.Tools),
		Stream:    false,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 2048
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return GenerateResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return GenerateResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", version)

	resp, err := client.Do(httpReq)
	if err != nil {
		return GenerateResponse{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return GenerateResponse{}, err
	}
	if resp.StatusCode >= 300 {
		return GenerateResponse{}, fmt.Errorf("anthropic api error: %s", strings.TrimSpace(string(raw)))
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return GenerateResponse{}, err
	}

	response := GenerateResponse{
		StopReason:   parsed.StopReason,
		ProviderName: "anthropic",
		Usage: runtime.Usage{
			InputTokens:  parsed.Usage.InputTokens,
			OutputTokens: parsed.Usage.OutputTokens,
		},
	}
	for _, block := range parsed.Content {
		switch block.Type {
		case "text":
			if response.AssistantText != "" {
				response.AssistantText += "\n"
			}
			response.AssistantText += block.Text
		case "tool_use":
			response.ToolCalls = append(response.ToolCalls, runtime.ToolCall{
				ID:              block.ID,
				Name:            block.Name,
				Input:           block.Input,
				ReadOnly:        false,
				ConcurrencySafe: false,
			})
		}
	}
	return response, nil
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func toAnthropicMessages(messages []runtime.Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		switch message.Kind {
		case runtime.MessageKindSystem:
			continue
		case runtime.MessageKindText:
			if message.Role != runtime.RoleUser && message.Role != runtime.RoleAssistant {
				continue
			}
			out = append(out, anthropicMessage{
				Role: string(message.Role),
				Content: []anthropicContentBlock{
					{Type: "text", Text: message.Content},
				},
			})
		case runtime.MessageKindToolCall:
			if message.ToolCall == nil {
				continue
			}
			out = append(out, anthropicMessage{
				Role: string(runtime.RoleAssistant),
				Content: []anthropicContentBlock{
					{
						Type:  "tool_use",
						ID:    message.ToolCall.ID,
						Name:  message.ToolCall.Name,
						Input: message.ToolCall.Input,
					},
				},
			})
		case runtime.MessageKindToolResult:
			if message.ToolResult == nil {
				continue
			}
			out = append(out, anthropicMessage{
				Role: string(runtime.RoleUser),
				Content: []anthropicContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: message.ToolResult.ToolCallID,
						Content:   message.ToolResult.Content,
						IsError:   message.ToolResult.IsError,
					},
				},
			})
		}
	}
	return out
}

func toAnthropicTools(tools []runtime.ToolDescriptor) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, anthropicTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}
	return out
}

func chooseModel(model string) string {
	if strings.TrimSpace(model) == "" {
		return "claude-3-5-sonnet-latest"
	}
	return model
}
