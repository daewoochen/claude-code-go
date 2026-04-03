package providers

import (
	"bufio"
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
		Stream:    request.OnAssistantDelta != nil,
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

	if resp.StatusCode >= 300 {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return GenerateResponse{}, err
		}
		return GenerateResponse{}, fmt.Errorf("anthropic api error: %s", strings.TrimSpace(string(raw)))
	}
	if body.Stream {
		return parseAnthropicStream(resp.Body, request.OnAssistantDelta, request.OnToolCall)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return GenerateResponse{}, err
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

type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type         string `json:"type"`
		Text         string `json:"text"`
		PartialJSON  string `json:"partial_json"`
		StopReason   string `json:"stop_reason"`
		StopSequence string `json:"stop_sequence"`
	} `json:"delta"`
	ContentBlock struct {
		Type  string         `json:"type"`
		Text  string         `json:"text"`
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	} `json:"content_block"`
	Message struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicStreamBlock struct {
	BlockType    string
	Text         strings.Builder
	ToolID       string
	ToolName     string
	ToolInput    map[string]any
	ToolInputRaw strings.Builder
	ToolSent     bool
}

func parseAnthropicStream(reader io.Reader, onAssistantDelta func(string), onToolCall func(runtime.ToolCall) error) (GenerateResponse, error) {
	response := GenerateResponse{
		ProviderName: "anthropic",
		StreamedText: onAssistantDelta != nil,
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 1024), 4*1024*1024)

	var (
		eventName string
		dataLines []string
		blocks    = map[int]*anthropicStreamBlock{}
		order     []int
	)

	flush := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
			eventName = ""
			return nil
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("parse anthropic stream event %q: %w", eventName, err)
		}
		switch event.Type {
		case "message_start":
			response.Usage.InputTokens = event.Message.Usage.InputTokens
			response.Usage.OutputTokens = event.Message.Usage.OutputTokens
		case "content_block_start":
			block := &anthropicStreamBlock{
				BlockType: event.ContentBlock.Type,
				ToolID:    event.ContentBlock.ID,
				ToolName:  event.ContentBlock.Name,
				ToolInput: event.ContentBlock.Input,
			}
			if block.BlockType == "text" && event.ContentBlock.Text != "" {
				block.Text.WriteString(event.ContentBlock.Text)
				if onAssistantDelta != nil {
					onAssistantDelta(event.ContentBlock.Text)
				}
			}
			if _, ok := blocks[event.Index]; !ok {
				order = append(order, event.Index)
			}
			blocks[event.Index] = block
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				block = &anthropicStreamBlock{}
				blocks[event.Index] = block
				order = append(order, event.Index)
			}
			switch event.Delta.Type {
			case "text_delta":
				block.BlockType = "text"
				block.Text.WriteString(event.Delta.Text)
				if onAssistantDelta != nil && event.Delta.Text != "" {
					onAssistantDelta(event.Delta.Text)
				}
			case "input_json_delta":
				block.BlockType = "tool_use"
				block.ToolInputRaw.WriteString(event.Delta.PartialJSON)
			}
		case "content_block_stop":
			if err := maybeDispatchStreamTool(blocks[event.Index], onToolCall); err != nil {
				return err
			}
		case "message_delta":
			if event.Delta.StopReason != "" {
				response.StopReason = event.Delta.StopReason
			}
			if event.Usage.OutputTokens > 0 {
				response.Usage.OutputTokens = event.Usage.OutputTokens
			}
		case "error":
			if event.Error.Message == "" {
				return fmt.Errorf("anthropic stream error: %s", event.Error.Type)
			}
			return fmt.Errorf("anthropic stream error: %s", event.Error.Message)
		}
		eventName = ""
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return GenerateResponse{}, err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return GenerateResponse{}, err
	}
	if err := flush(); err != nil {
		return GenerateResponse{}, err
	}

	for _, index := range order {
		block := blocks[index]
		if block == nil {
			continue
		}
		switch block.BlockType {
		case "text":
			response.AssistantText += block.Text.String()
		case "tool_use":
			if err := maybeDispatchStreamTool(block, onToolCall); err != nil {
				return GenerateResponse{}, err
			}
			response.ToolCalls = append(response.ToolCalls, runtime.ToolCall{
				ID:              block.ToolID,
				Name:            block.ToolName,
				Input:           normalizedToolInput(block),
				ReadOnly:        false,
				ConcurrencySafe: false,
			})
		}
	}

	if response.StopReason == "" {
		response.StopReason = "end_turn"
	}
	return response, nil
}

func maybeDispatchStreamTool(block *anthropicStreamBlock, onToolCall func(runtime.ToolCall) error) error {
	if block == nil || block.BlockType != "tool_use" || block.ToolSent {
		return nil
	}
	block.ToolInput = normalizedToolInput(block)
	if onToolCall != nil {
		if err := onToolCall(runtime.ToolCall{
			ID:              block.ToolID,
			Name:            block.ToolName,
			Input:           block.ToolInput,
			ReadOnly:        false,
			ConcurrencySafe: false,
		}); err != nil {
			return err
		}
	}
	block.ToolSent = true
	return nil
}

func normalizedToolInput(block *anthropicStreamBlock) map[string]any {
	if block == nil {
		return map[string]any{}
	}
	input := block.ToolInput
	if input == nil {
		input = map[string]any{}
	}
	if raw := strings.TrimSpace(block.ToolInputRaw.String()); raw != "" {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			input = decoded
		}
	}
	return input
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
