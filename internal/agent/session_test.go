package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/daewoochen/claude-code-go/internal/providers"
	"github.com/daewoochen/claude-code-go/internal/runtime"
	"github.com/daewoochen/claude-code-go/internal/session"
	"github.com/daewoochen/claude-code-go/internal/tools"
)

func TestSessionRunTurnPlainText(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(ctx, Config{
		CWD:      t.TempDir(),
		Store:    store,
		Provider: providers.MockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "hello world", runtime.RunOptions{CWD: t.TempDir()}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if got, want := result.Result, "mock: hello world"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestSessionRunTurnToolLoopAndDenial(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	store, err := session.NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(ctx, Config{
		CWD:            tmp,
		Store:          store,
		Provider:       providers.MockProvider{},
		PermissionMode: runtime.PermissionModeAskAsError,
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "run pwd", runtime.RunOptions{
		CWD:            tmp,
		PermissionMode: runtime.PermissionModeAskAsError,
	}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if len(result.PermissionDenials) != 1 {
		t.Fatalf("permission denials = %d, want 1", len(result.PermissionDenials))
	}
	if result.Result == "" {
		t.Fatal("expected final result after tool denial")
	}
}

func TestSessionRunTurnReadFile(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	target := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(target, []byte("hello from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(filepath.Join(tmp, "state"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(ctx, Config{
		CWD:      tmp,
		Store:    store,
		Provider: providers.MockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "read note.txt", runtime.RunOptions{CWD: tmp}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestSessionSlashToolsCommand(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(ctx, Config{
		CWD:      t.TempDir(),
		Store:    store,
		Provider: providers.MockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "/tools", runtime.RunOptions{CWD: t.TempDir()}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Turns != 0 {
		t.Fatalf("turns = %d, want 0 for local slash command", result.Turns)
	}
	if got := result.Result; got == "" || !containsAll(got, "echo", "read_file", "bash") {
		t.Fatalf("unexpected result text: %q", got)
	}
}

func TestSessionPromptTooLongRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &recoveringProvider{
		firstErr: errors.New("prompt is too long for this model"),
		then: providers.GenerateResponse{
			AssistantText: "recovered after compact",
			StopReason:    "end_turn",
			ProviderName:  "test",
		},
	}
	s, err := NewSession(ctx, Config{
		CWD:      t.TempDir(),
		Store:    store,
		Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	longInput := ""
	for i := 0; i < 40; i++ {
		longInput += "long message segment "
	}
	var result runtime.RunResult
	for event := range s.RunTurn(ctx, longInput, runtime.RunOptions{CWD: t.TempDir()}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if provider.calls < 2 {
		t.Fatalf("provider calls = %d, want at least 2", provider.calls)
	}
	state := s.State()
	if state.LastCompactionReason != "prompt_too_long" {
		t.Fatalf("compaction reason = %q, want prompt_too_long", state.LastCompactionReason)
	}
}

func TestSessionMaxOutputRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &sequenceProvider{
		responses: []providers.GenerateResponse{
			{
				AssistantText: "partial answer",
				StopReason:    "max_tokens",
				ProviderName:  "test",
			},
			{
				AssistantText: "continued answer",
				StopReason:    "end_turn",
				ProviderName:  "test",
			},
		},
	}
	s, err := NewSession(ctx, Config{
		CWD:      t.TempDir(),
		Store:    store,
		Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "tell me a long story", runtime.RunOptions{CWD: t.TempDir()}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
	if got, want := result.Result, "continued answer"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestSessionStreamingToolDispatch(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	var executed atomic.Int32
	registry.Register(tools.Definition{
		Descriptor: runtime.ToolDescriptor{
			Name:            "stream_probe",
			Description:     "Probe streaming tool dispatch.",
			InputSchema:     map[string]any{"type": "object"},
			ReadOnly:        false,
			ConcurrencySafe: false,
			Source:          "test",
		},
		InterruptBehavior: tools.InterruptBehaviorCancel,
		Execute: func(ctx context.Context, execCtx tools.ExecutionContext, input map[string]any, report tools.ProgressReporter) (runtime.ToolResult, error) {
			_ = ctx
			_ = execCtx
			_ = input
			report("stream probe executed")
			executed.Add(1)
			return runtime.ToolResult{Content: "probe ok"}, nil
		},
	})
	provider := &streamingToolProvider{executed: &executed}
	s, err := NewSession(ctx, Config{
		CWD:            t.TempDir(),
		Store:          store,
		Provider:       provider,
		Registry:       registry,
		PermissionMode: runtime.PermissionModeAllowAll,
	})
	if err != nil {
		t.Fatal(err)
	}

	var (
		result      runtime.RunResult
		toolResults int
	)
	for event := range s.RunTurn(ctx, "please stream a tool", runtime.RunOptions{
		CWD:            t.TempDir(),
		PermissionMode: runtime.PermissionModeAllowAll,
	}) {
		if event.Type == runtime.EventToolResult {
			toolResults++
		}
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
	if got := executed.Load(); got != 1 {
		t.Fatalf("tool executions = %d, want 1", got)
	}
	if toolResults != 1 {
		t.Fatalf("tool result events = %d, want 1", toolResults)
	}
	if got, want := result.Result, "streamed tool finished"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

type recoveringProvider struct {
	calls    int
	firstErr error
	then     providers.GenerateResponse
}

func (p *recoveringProvider) Name() string { return "recovering" }

func (p *recoveringProvider) Generate(ctx context.Context, request providers.GenerateRequest) (providers.GenerateResponse, error) {
	_ = ctx
	_ = request
	p.calls++
	if p.calls == 1 {
		return providers.GenerateResponse{}, p.firstErr
	}
	return p.then, nil
}

type sequenceProvider struct {
	calls     int
	responses []providers.GenerateResponse
}

func (p *sequenceProvider) Name() string { return "sequence" }

func (p *sequenceProvider) Generate(ctx context.Context, request providers.GenerateRequest) (providers.GenerateResponse, error) {
	_ = ctx
	_ = request
	if p.calls >= len(p.responses) {
		return providers.GenerateResponse{}, errors.New("no more responses")
	}
	resp := p.responses[p.calls]
	p.calls++
	if request.OnAssistantDelta != nil && resp.AssistantText != "" {
		request.OnAssistantDelta(resp.AssistantText)
		resp.StreamedText = true
	}
	return resp, nil
}

type streamingToolProvider struct {
	calls    int
	executed *atomic.Int32
}

func (p *streamingToolProvider) Name() string { return "streaming-tool" }

func (p *streamingToolProvider) Generate(ctx context.Context, request providers.GenerateRequest) (providers.GenerateResponse, error) {
	_ = ctx
	if p.calls == 0 {
		if request.OnToolCall == nil {
			return providers.GenerateResponse{}, errors.New("missing OnToolCall callback")
		}
		call := runtime.ToolCall{
			ID:   "stream_call_1",
			Name: "stream_probe",
			Input: map[string]any{
				"task": "probe",
			},
		}
		if err := request.OnToolCall(call); err != nil {
			return providers.GenerateResponse{}, err
		}
		if p.executed.Load() == 0 {
			return providers.GenerateResponse{}, errors.New("tool was not executed during streaming callback")
		}
		p.calls++
		return providers.GenerateResponse{
			ToolCalls:    []runtime.ToolCall{call},
			StopReason:   "tool_use",
			ProviderName: "stream-test",
		}, nil
	}
	p.calls++
	if request.OnAssistantDelta != nil {
		request.OnAssistantDelta("streamed tool finished")
	}
	return providers.GenerateResponse{
		AssistantText: "streamed tool finished",
		StopReason:    "end_turn",
		ProviderName:  "stream-test",
		StreamedText:  request.OnAssistantDelta != nil,
	}, nil
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
