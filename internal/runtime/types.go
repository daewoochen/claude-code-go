package runtime

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type MessageKind string

const (
	MessageKindText       MessageKind = "text"
	MessageKindToolCall   MessageKind = "tool_call"
	MessageKindToolResult MessageKind = "tool_result"
	MessageKindSystem     MessageKind = "system"
	MessageKindAttachment MessageKind = "attachment"
)

type PermissionMode string

const (
	PermissionModeAllowAll   PermissionMode = "allow_all"
	PermissionModeDenyAll    PermissionMode = "deny_all"
	PermissionModeAskAsError PermissionMode = "ask_as_error"
)

type InterruptMetadata struct {
	Interrupted bool      `json:"interrupted"`
	Reason      string    `json:"reason,omitempty"`
	At          time.Time `json:"at,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type BudgetState struct {
	MaxUSD  float64 `json:"max_usd"`
	UsedUSD float64 `json:"used_usd"`
}

type ToolDescriptor struct {
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	InputSchema     map[string]any    `json:"input_schema,omitempty"`
	ReadOnly        bool              `json:"read_only"`
	ConcurrencySafe bool              `json:"concurrency_safe"`
	Source          string            `json:"source,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type ToolCall struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Input           map[string]any    `json:"input,omitempty"`
	ReadOnly        bool              `json:"read_only"`
	ConcurrencySafe bool              `json:"concurrency_safe"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Raw             map[string]any    `json:"raw,omitempty"`
}

type ToolResult struct {
	ToolCallID string            `json:"tool_call_id"`
	Name       string            `json:"name"`
	Content    string            `json:"content"`
	IsError    bool              `json:"is_error"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type PermissionDenial struct {
	ToolName   string         `json:"tool_name"`
	ToolCallID string         `json:"tool_call_id"`
	Input      map[string]any `json:"input,omitempty"`
	Reason     string         `json:"reason,omitempty"`
}

type Message struct {
	ID         string            `json:"id"`
	Role       Role              `json:"role"`
	Kind       MessageKind       `json:"kind"`
	Content    string            `json:"content,omitempty"`
	ToolCall   *ToolCall         `json:"tool_call,omitempty"`
	ToolResult *ToolResult       `json:"tool_result,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

type SessionState struct {
	SessionID              string             `json:"session_id"`
	Messages               []Message          `json:"messages"`
	ReadFileCache          map[string]string  `json:"read_file_cache,omitempty"`
	Usage                  Usage              `json:"usage"`
	Budget                 BudgetState        `json:"budget"`
	MaxTurns               int                `json:"max_turns"`
	TurnsUsed              int                `json:"turns_used"`
	PendingToolCalls       []ToolCall         `json:"pending_tool_calls,omitempty"`
	PendingToolSummary     string             `json:"pending_tool_summary,omitempty"`
	DiscoveredSkills       []string           `json:"discovered_skills,omitempty"`
	Interrupt              InterruptMetadata  `json:"interrupt"`
	PermissionMode         PermissionMode     `json:"permission_mode"`
	PermissionDenials      []PermissionDenial `json:"permission_denials,omitempty"`
	SystemPrompt           string             `json:"system_prompt,omitempty"`
	AppendSystemPrompt     string             `json:"append_system_prompt,omitempty"`
	Model                  string             `json:"model,omitempty"`
	PendingInput           string             `json:"pending_input,omitempty"`
	NeedModelCall          bool               `json:"need_model_call"`
	Continue               bool               `json:"continue"`
	Completed              bool               `json:"completed"`
	Error                  string             `json:"error,omitempty"`
	LastResult             string             `json:"last_result,omitempty"`
	LastStopReason         string             `json:"last_stop_reason,omitempty"`
	CurrentIteration       int                `json:"current_iteration"`
	CheckpointID           string             `json:"checkpoint_id,omitempty"`
	ToolResultBudget       int                `json:"tool_result_budget"`
	AdditionalCWDs         []string           `json:"additional_cwds,omitempty"`
	Metadata               map[string]string  `json:"metadata,omitempty"`
	FallbackModel          string             `json:"fallback_model,omitempty"`
	StructuredJSON         json.RawMessage    `json:"structured_json,omitempty"`
	LastProvider           string             `json:"last_provider,omitempty"`
	ResumeFromCheckpoint   bool               `json:"resume_from_checkpoint,omitempty"`
	ReactiveCompactCount   int                `json:"reactive_compact_count,omitempty"`
	MaxOutputRecoveryCount int                `json:"max_output_recovery_count,omitempty"`
	LastCompactionReason   string             `json:"last_compaction_reason,omitempty"`
}

type TranscriptRecord struct {
	Type      string        `json:"type"`
	SessionID string        `json:"session_id"`
	Timestamp time.Time     `json:"timestamp"`
	Message   *Message      `json:"message,omitempty"`
	State     *SessionState `json:"state,omitempty"`
	Note      string        `json:"note,omitempty"`
}

type RunOptions struct {
	CWD                string
	Model              string
	MaxTurns           int
	SessionID          string
	AppendSystemPrompt string
	StrictMCPConfig    bool
	OutputMode         string
	PermissionMode     PermissionMode
	MaxBudgetUSD       float64
	CheckpointID       string
	FallbackModel      string
}

type RunResult struct {
	SessionID         string             `json:"session_id"`
	Result            string             `json:"result"`
	Reason            string             `json:"reason"`
	Error             string             `json:"error,omitempty"`
	Turns             int                `json:"turns"`
	Usage             Usage              `json:"usage"`
	PermissionDenials []PermissionDenial `json:"permission_denials,omitempty"`
}
