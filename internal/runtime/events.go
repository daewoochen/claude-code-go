package runtime

import "time"

type EventType string

const (
	EventSystem         EventType = "system"
	EventAssistantDelta EventType = "assistant_delta"
	EventAssistant      EventType = "assistant"
	EventToolProgress   EventType = "tool_progress"
	EventToolResult     EventType = "tool_result"
	EventAttachment     EventType = "attachment"
	EventResult         EventType = "result"
)

type Event struct {
	Type       EventType         `json:"type"`
	SessionID  string            `json:"session_id"`
	Message    string            `json:"message,omitempty"`
	Delta      string            `json:"delta,omitempty"`
	ToolName   string            `json:"tool_name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Result     *RunResult        `json:"result,omitempty"`
	Data       map[string]string `json:"data,omitempty"`
	At         time.Time         `json:"at"`
}
