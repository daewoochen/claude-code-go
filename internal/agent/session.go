package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"

	"github.com/daewoochen/claude-code-go/internal/mcp"
	"github.com/daewoochen/claude-code-go/internal/prompts"
	"github.com/daewoochen/claude-code-go/internal/providers"
	"github.com/daewoochen/claude-code-go/internal/runtime"
	"github.com/daewoochen/claude-code-go/internal/session"
	"github.com/daewoochen/claude-code-go/internal/tools"
)

type Config struct {
	SessionID          string
	CWD                string
	Model              string
	MaxTurns           int
	PermissionMode     runtime.PermissionMode
	Store              *session.Store
	Provider           providers.ChatModelProvider
	Registry           *tools.Registry
	MCP                *mcp.Manager
	AppendSystemPrompt string
	FallbackModel      string
	ToolResultBudget   int
}

type Session struct {
	mu       sync.Mutex
	config   Config
	store    *session.Store
	provider providers.ChatModelProvider
	registry *tools.Registry
	mcp      *mcp.Manager
	graph    compose.Runnable[*runtime.SessionState, *runtime.SessionState]
	state    *runtime.SessionState
}

func NewSession(ctx context.Context, cfg Config) (*Session, error) {
	store := cfg.Store
	if store == nil {
		var err error
		store, err = session.NewStore("")
		if err != nil {
			return nil, err
		}
	}
	registry := cfg.Registry
	if registry == nil {
		registry = tools.NewRegistry()
		registry.RegisterAll(tools.Builtins()...)
	}
	if cfg.MCP != nil {
		registry.AddDynamicSource(dynamicMCPSource{manager: cfg.MCP})
		if err := cfg.MCP.Refresh(ctx); err != nil {
			return nil, err
		}
	}
	provider := cfg.Provider
	if provider == nil {
		provider = providers.MockProvider{}
	}
	if cfg.SessionID == "" {
		cfg.SessionID = generateSessionID()
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 8
	}
	if cfg.ToolResultBudget <= 0 {
		cfg.ToolResultBudget = 64 * 1024
	}
	if cfg.PermissionMode == "" {
		cfg.PermissionMode = runtime.PermissionModeAskAsError
	}
	state := &runtime.SessionState{
		SessionID:          cfg.SessionID,
		ReadFileCache:      map[string]string{},
		MaxTurns:           cfg.MaxTurns,
		PermissionMode:     cfg.PermissionMode,
		Model:              cfg.Model,
		AppendSystemPrompt: cfg.AppendSystemPrompt,
		CheckpointID:       cfg.SessionID,
		ToolResultBudget:   cfg.ToolResultBudget,
		Metadata: map[string]string{
			"cwd": cfg.CWD,
		},
		FallbackModel: cfg.FallbackModel,
	}
	s := &Session{
		config:   cfg,
		store:    store,
		provider: provider,
		registry: registry,
		mcp:      cfg.MCP,
		state:    state,
	}
	graph, err := s.buildGraph(ctx)
	if err != nil {
		return nil, err
	}
	s.graph = graph
	return s, nil
}

func ResumeSession(ctx context.Context, cfg Config, sessionID string) (*Session, error) {
	cfg.SessionID = sessionID
	s, err := NewSession(ctx, cfg)
	if err != nil {
		return nil, err
	}
	loaded, err := s.store.ResumeFromCheckpoint(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if loaded.Metadata == nil {
		loaded.Metadata = map[string]string{}
	}
	if loaded.Metadata["cwd"] == "" {
		loaded.Metadata["cwd"] = cfg.CWD
	}
	s.state = loaded
	return s, nil
}

func (s *Session) State() runtime.SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.state
}

func (s *Session) RunTurn(ctx context.Context, input string, opts runtime.RunOptions) <-chan runtime.Event {
	ch := make(chan runtime.Event, 64)
	go func() {
		defer close(ch)
		sink := func(event runtime.Event) {
			if event.SessionID == "" {
				event.SessionID = s.state.SessionID
			}
			ch <- event
		}
		runCtx := runtime.WithEventSink(ctx, sink)
		result := s.runTurn(runCtx, input, opts)
		runtime.Emit(runCtx, runtime.Event{
			Type:      runtime.EventResult,
			SessionID: result.SessionID,
			Result:    &result,
		})
	}()
	return ch
}

func (s *Session) runTurn(ctx context.Context, input string, opts runtime.RunOptions) runtime.RunResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.applyOptions(opts)

	if strings.TrimSpace(input) != "" {
		processed, err := s.processInput(ctx, input)
		if err != nil {
			s.state.Error = err.Error()
			s.state.NeedModelCall = false
			s.state.Completed = true
			s.state.Continue = false
		} else {
			if processed.clearFirst {
				s.state.Messages = nil
				s.state.PermissionDenials = nil
				s.state.PendingToolCalls = nil
				s.state.LastResult = ""
				s.state.Error = ""
				s.state.ReactiveCompactCount = 0
				s.state.MaxOutputRecoveryCount = 0
			}
			if len(processed.messages) > 0 {
				s.state.Messages = append(s.state.Messages, processed.messages...)
				records := make([]runtime.TranscriptRecord, 0, len(processed.messages))
				for _, message := range processed.messages {
					msg := message
					records = append(records, runtime.TranscriptRecord{
						Type:      "message",
						SessionID: s.state.SessionID,
						Message:   &msg,
					})
				}
				_ = s.store.Append(ctx, s.state.SessionID, records...)
			}
			s.state.NeedModelCall = processed.shouldQuery
			s.state.Completed = !processed.shouldQuery
			s.state.Continue = false
			s.state.Error = ""
			if processed.resultText != "" {
				s.state.LastResult = processed.resultText
				runtime.Emit(ctx, runtime.Event{
					Type:      runtime.EventAssistantDelta,
					SessionID: s.state.SessionID,
					Delta:     processed.resultText,
				})
				runtime.Emit(ctx, runtime.Event{
					Type:      runtime.EventAssistant,
					SessionID: s.state.SessionID,
					Message:   processed.resultText,
				})
			}
		}
	}

	for {
		if s.state.NeedModelCall && s.state.TurnsUsed >= s.state.MaxTurns {
			s.state.Error = fmt.Sprintf("max turns reached: %d", s.state.MaxTurns)
			s.state.Completed = true
			s.state.Continue = false
			break
		}

		before := len(s.state.Messages)
		next, err := s.graph.Invoke(
			ctx,
			s.state,
			compose.WithCheckPointID(s.state.CheckpointID),
			compose.WithForceNewRun(),
		)
		if err != nil {
			s.state.Error = err.Error()
			s.state.Completed = true
			s.state.Continue = false
			break
		}
		s.state = next
		if len(s.state.Messages) > before {
			records := make([]runtime.TranscriptRecord, 0, len(s.state.Messages)-before)
			for _, message := range s.state.Messages[before:] {
				msg := message
				records = append(records, runtime.TranscriptRecord{
					Type:      "message",
					SessionID: s.state.SessionID,
					Message:   &msg,
				})
			}
			_ = s.store.Append(ctx, s.state.SessionID, records...)
		}
		_ = s.store.SaveSnapshot(ctx, s.state)
		if s.state.Completed || !s.state.Continue {
			break
		}
	}

	reason := "completed"
	if s.state.Error != "" {
		reason = "error"
	}
	result := runtime.RunResult{
		SessionID:         s.state.SessionID,
		Result:            s.state.LastResult,
		Reason:            reason,
		Error:             s.state.Error,
		Turns:             s.state.TurnsUsed,
		Usage:             s.state.Usage,
		PermissionDenials: s.state.PermissionDenials,
	}
	_ = s.store.SaveSnapshot(ctx, s.state)
	return result
}

func (s *Session) applyOptions(opts runtime.RunOptions) {
	if opts.CWD != "" {
		if s.state.Metadata == nil {
			s.state.Metadata = map[string]string{}
		}
		s.state.Metadata["cwd"] = opts.CWD
	}
	if opts.Model != "" {
		s.state.Model = opts.Model
	}
	if opts.MaxTurns > 0 {
		s.state.MaxTurns = opts.MaxTurns
	}
	if opts.SessionID != "" {
		s.state.SessionID = opts.SessionID
	}
	if opts.AppendSystemPrompt != "" {
		s.state.AppendSystemPrompt = opts.AppendSystemPrompt
	}
	if opts.PermissionMode != "" {
		s.state.PermissionMode = opts.PermissionMode
	}
	if opts.CheckpointID != "" {
		s.state.CheckpointID = opts.CheckpointID
	}
	if opts.FallbackModel != "" {
		s.state.FallbackModel = opts.FallbackModel
	}
}

type dynamicMCPSource struct {
	manager *mcp.Manager
}

func (d dynamicMCPSource) Refresh(ctx context.Context) ([]tools.Definition, error) {
	return d.manager.RefreshTools(ctx)
}

func generateSessionID() string {
	return fmt.Sprintf("session_%d", time.Now().UnixNano())
}

func generateMessageID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func (s *Session) systemPrompt() string {
	return prompts.BuildSystemPrompt(s.state.AppendSystemPrompt)
}
