package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"

	"github.com/daewoochen/claude-code-go/internal/providers"
	"github.com/daewoochen/claude-code-go/internal/runtime"
	"github.com/daewoochen/claude-code-go/internal/tools"
)

func (s *Session) buildGraph(ctx context.Context) (compose.Runnable[*runtime.SessionState, *runtime.SessionState], error) {
	g := compose.NewGraph[*runtime.SessionState, *runtime.SessionState]()

	_ = g.AddLambdaNode("input_normalize", compose.InvokableLambda(s.inputNormalizeNode))
	_ = g.AddLambdaNode("system_prompt_assemble", compose.InvokableLambda(s.systemPromptNode))
	_ = g.AddLambdaNode("message_rewrite", compose.InvokableLambda(s.messageRewriteNode))
	_ = g.AddLambdaNode("model_call", compose.InvokableLambda(s.modelCallNode))
	_ = g.AddLambdaNode("tool_dispatch", compose.InvokableLambda(s.toolDispatchNode))
	_ = g.AddLambdaNode("attachment_memory_inject", compose.InvokableLambda(s.attachmentNode))
	_ = g.AddLambdaNode("stop_budget_check", compose.InvokableLambda(s.stopBudgetNode))
	_ = g.AddLambdaNode("continue_or_finish", compose.InvokableLambda(s.continueNode))

	_ = g.AddEdge(compose.START, "input_normalize")
	_ = g.AddEdge("input_normalize", "system_prompt_assemble")
	_ = g.AddEdge("system_prompt_assemble", "message_rewrite")
	_ = g.AddEdge("message_rewrite", "model_call")
	_ = g.AddEdge("model_call", "tool_dispatch")
	_ = g.AddEdge("tool_dispatch", "attachment_memory_inject")
	_ = g.AddEdge("attachment_memory_inject", "stop_budget_check")
	_ = g.AddEdge("stop_budget_check", "continue_or_finish")
	_ = g.AddEdge("continue_or_finish", compose.END)

	return g.Compile(ctx, compose.WithGraphName("ccgo_agent_loop"), compose.WithCheckPointStore(s.store))
}

func (s *Session) inputNormalizeNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	if state.Metadata == nil {
		state.Metadata = map[string]string{}
	}
	state.CurrentIteration++
	if state.ToolResultBudget == 0 {
		state.ToolResultBudget = s.config.ToolResultBudget
	}
	if state.ToolResultBudget == 0 {
		state.ToolResultBudget = 64 * 1024
	}
	if state.Model == "" {
		state.Model = s.config.Model
	}
	if state.MaxTurns == 0 {
		state.MaxTurns = s.config.MaxTurns
	}
	if state.MaxTurns == 0 {
		state.MaxTurns = 8
	}
	if state.PermissionMode == "" {
		state.PermissionMode = s.config.PermissionMode
	}
	if state.PermissionMode == "" {
		state.PermissionMode = runtime.PermissionModeAskAsError
	}
	if state.Metadata["cwd"] == "" {
		state.Metadata["cwd"] = s.config.CWD
	}
	return state, nil
}

func (s *Session) systemPromptNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	_ = ctx
	state.SystemPrompt = s.systemPrompt()
	return state, nil
}

func (s *Session) messageRewriteNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	_ = ctx
	total := 0
	for _, message := range state.Messages {
		if message.Kind == runtime.MessageKindToolResult {
			total += len(message.Content)
		}
	}
	if total <= state.ToolResultBudget {
		return state, nil
	}
	remaining := total
	for i := range state.Messages {
		if state.Messages[i].Kind != runtime.MessageKindToolResult {
			continue
		}
		if remaining <= state.ToolResultBudget {
			break
		}
		original := state.Messages[i].Content
		if len(original) <= 96 {
			continue
		}
		truncated := original[:96] + "\n[tool result truncated by ccgo]"
		state.Messages[i].Content = truncated
		if state.Messages[i].ToolResult != nil {
			state.Messages[i].ToolResult.Content = truncated
		}
		remaining -= len(original) - len(truncated)
	}
	return state, nil
}

func (s *Session) modelCallNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	if !state.NeedModelCall || state.Error != "" {
		return state, nil
	}
	if err := s.registry.Refresh(ctx); err != nil {
		state.Error = err.Error()
		return state, nil
	}
	runtime.Emit(ctx, runtime.Event{
		Type:      runtime.EventSystem,
		SessionID: state.SessionID,
		Message:   "calling model",
	})

	executor := tools.Executor{
		Registry: s.registry,
		Policy: tools.Policy{
			Mode: state.PermissionMode,
		},
	}
	execCtx := tools.ExecutionContext{
		SessionID: state.SessionID,
		CWD:       state.Metadata["cwd"],
	}
	type streamedExecution struct {
		call    runtime.ToolCall
		updates []tools.Update
	}
	var (
		streamMu        sync.Mutex
		streamWait      sync.WaitGroup
		streamed        []streamedExecution
		streamedExecErr error
	)

	request := providersRequestFromState(state, s.registry.Descriptors())
	request.OnAssistantDelta = func(delta string) {
		if strings.TrimSpace(delta) == "" && delta != "\n" {
			return
		}
		runtime.Emit(ctx, runtime.Event{
			Type:      runtime.EventAssistantDelta,
			SessionID: state.SessionID,
			Delta:     delta,
		})
	}
	request.OnToolCall = func(call runtime.ToolCall) error {
		call = s.normalizeToolCall(call)
		if call.Metadata == nil {
			call.Metadata = map[string]string{}
		}
		call.Metadata["dispatch"] = "streaming"

		streamMu.Lock()
		index := len(streamed)
		streamed = append(streamed, streamedExecution{call: call})
		streamMu.Unlock()

		run := func() {
			updates, err := executor.ExecuteBatch(ctx, execCtx, []runtime.ToolCall{call})
			streamMu.Lock()
			defer streamMu.Unlock()
			streamed[index].updates = updates
			if err != nil && streamedExecErr == nil {
				streamedExecErr = err
			}
		}
		if call.ConcurrencySafe {
			streamWait.Add(1)
			go func() {
				defer streamWait.Done()
				run()
			}()
			return nil
		}
		streamWait.Wait()
		run()
		return streamedExecErr
	}
	response, err := s.provider.Generate(ctx, request)
	streamWait.Wait()
	if err != nil && state.FallbackModel != "" && state.FallbackModel != state.Model {
		runtime.Emit(ctx, runtime.Event{
			Type:      runtime.EventSystem,
			SessionID: state.SessionID,
			Message:   fmt.Sprintf("primary model failed, retrying with %s", state.FallbackModel),
		})
		request.Model = state.FallbackModel
		response, err = s.provider.Generate(ctx, request)
		streamWait.Wait()
	}
	if err != nil {
		if isPromptTooLongError(err) && state.ReactiveCompactCount < maxReactiveCompactAttempts {
			if compactStateForRetry(state, "prompt_too_long") {
				runtime.Emit(ctx, runtime.Event{
					Type:      runtime.EventSystem,
					SessionID: state.SessionID,
					Message:   "context too large, compacting history and retrying",
				})
				state.NeedModelCall = true
				state.Continue = true
				state.Completed = false
				state.Error = ""
				return state, nil
			}
		}
		state.Error = err.Error()
		return state, nil
	}
	if streamedExecErr != nil {
		state.Error = streamedExecErr.Error()
		return state, nil
	}
	state.TurnsUsed++
	state.Usage.InputTokens += response.Usage.InputTokens
	state.Usage.OutputTokens += response.Usage.OutputTokens
	state.LastProvider = response.ProviderName
	state.LastStopReason = response.StopReason
	state.ReactiveCompactCount = 0
	state.NeedModelCall = false

	if strings.TrimSpace(response.AssistantText) != "" {
		text := strings.TrimSpace(response.AssistantText)
		if !response.StreamedText {
			for _, chunk := range chunkText(text, 80) {
				runtime.Emit(ctx, runtime.Event{
					Type:      runtime.EventAssistantDelta,
					SessionID: state.SessionID,
					Delta:     chunk,
				})
			}
		}
		message := runtime.Message{
			ID:        generateMessageID("assistant"),
			Role:      runtime.RoleAssistant,
			Kind:      runtime.MessageKindText,
			Content:   text,
			CreatedAt: time.Now().UTC(),
		}
		state.Messages = append(state.Messages, message)
		state.LastResult = text
		runtime.Emit(ctx, runtime.Event{
			Type:      runtime.EventAssistant,
			SessionID: state.SessionID,
			Message:   text,
		})
	}

	if isMaxOutputStopReason(response.StopReason) && len(response.ToolCalls) == 0 && state.MaxOutputRecoveryCount < maxOutputRecoveryAttempts {
		appendContinuationMessage(state)
		runtime.Emit(ctx, runtime.Event{
			Type:      runtime.EventSystem,
			SessionID: state.SessionID,
			Message:   "model hit max output tokens, automatically continuing",
		})
		state.NeedModelCall = true
		state.Continue = true
		state.Completed = false
		return state, nil
	}
	state.MaxOutputRecoveryCount = 0

	if len(response.ToolCalls) > 0 {
		state.LastResult = ""
		streamedByID := make(map[string]streamedExecution, len(streamed))
		for _, record := range streamed {
			streamedByID[record.call.ID] = record
		}
		handled := make(map[string]bool, len(streamedByID))
		pending := make([]runtime.ToolCall, 0, len(response.ToolCalls))
		for _, rawCall := range response.ToolCalls {
			call := s.normalizeToolCall(rawCall)
			state.Messages = append(state.Messages, assistantToolCallMessage(call))
			if record, ok := streamedByID[call.ID]; ok {
				handled[call.ID] = true
				s.applyToolUpdates(ctx, state, record.updates)
				continue
			}
			pending = append(pending, call)
		}
		for _, record := range streamed {
			if handled[record.call.ID] {
				continue
			}
			state.Messages = append(state.Messages, assistantToolCallMessage(record.call))
			s.applyToolUpdates(ctx, state, record.updates)
		}
		state.PendingToolCalls = pending
		if len(pending) == 0 && len(streamed) > 0 {
			state.NeedModelCall = true
		}
	} else if len(streamed) > 0 {
		state.LastResult = ""
		for _, record := range streamed {
			state.Messages = append(state.Messages, assistantToolCallMessage(record.call))
			s.applyToolUpdates(ctx, state, record.updates)
		}
		state.PendingToolCalls = nil
		state.NeedModelCall = true
	}
	return state, nil
}

func (s *Session) toolDispatchNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	if len(state.PendingToolCalls) == 0 || state.Error != "" {
		return state, nil
	}
	executor := tools.Executor{
		Registry: s.registry,
		Policy: tools.Policy{
			Mode: state.PermissionMode,
		},
	}
	updates, err := executor.ExecuteBatch(ctx, tools.ExecutionContext{
		SessionID: state.SessionID,
		CWD:       state.Metadata["cwd"],
	}, state.PendingToolCalls)
	if err != nil {
		state.Error = err.Error()
		return state, nil
	}
	s.applyToolUpdates(ctx, state, updates)
	state.PendingToolCalls = nil
	state.NeedModelCall = true
	return state, nil
}

func (s *Session) attachmentNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	if s.mcp == nil {
		return state, nil
	}
	if err := s.mcp.Refresh(ctx); err != nil {
		runtime.Emit(ctx, runtime.Event{
			Type:      runtime.EventSystem,
			SessionID: state.SessionID,
			Message:   "mcp refresh failed: " + err.Error(),
		})
	}
	return state, nil
}

func (s *Session) stopBudgetNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	_ = ctx
	if state.Budget.MaxUSD > 0 && state.Budget.UsedUSD >= state.Budget.MaxUSD {
		state.Error = "task budget exhausted"
		state.Completed = true
		state.Continue = false
		return state, nil
	}
	if state.NeedModelCall && state.TurnsUsed >= state.MaxTurns {
		state.Error = fmt.Sprintf("max turns reached: %d", state.MaxTurns)
		state.Completed = true
		state.Continue = false
	}
	return state, nil
}

func (s *Session) continueNode(ctx context.Context, state *runtime.SessionState) (*runtime.SessionState, error) {
	_ = ctx
	if state.Error != "" {
		state.Completed = true
		state.Continue = false
		return state, nil
	}
	if state.NeedModelCall {
		state.Completed = false
		state.Continue = true
		return state, nil
	}
	state.Completed = true
	state.Continue = false
	return state, nil
}

func providersRequestFromState(state *runtime.SessionState, descriptors []runtime.ToolDescriptor) providers.GenerateRequest {
	return providers.GenerateRequest{
		Model:           state.Model,
		SystemPrompt:    state.SystemPrompt,
		Messages:        state.Messages,
		Tools:           descriptors,
		MaxOutputTokens: 2048,
	}
}

func chunkText(text string, size int) []string {
	if size <= 0 || len(text) <= size {
		return []string{text}
	}
	chunks := make([]string, 0, (len(text)/size)+1)
	for len(text) > size {
		chunks = append(chunks, text[:size])
		text = text[size:]
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

func (s *Session) normalizeToolCall(call runtime.ToolCall) runtime.ToolCall {
	if call.ID == "" {
		call.ID = generateMessageID("tool")
	}
	if definition, ok := s.registry.Lookup(call.Name); ok {
		call.ReadOnly = definition.Descriptor.ReadOnly
		call.ConcurrencySafe = definition.Descriptor.ConcurrencySafe
		if call.Metadata == nil {
			call.Metadata = map[string]string{}
		}
		if definition.Descriptor.Source != "" && call.Metadata["source"] == "" {
			call.Metadata["source"] = definition.Descriptor.Source
		}
	}
	return call
}

func assistantToolCallMessage(call runtime.ToolCall) runtime.Message {
	callCopy := call
	return runtime.Message{
		ID:        generateMessageID("assistant_tool"),
		Role:      runtime.RoleAssistant,
		Kind:      runtime.MessageKindToolCall,
		ToolCall:  &callCopy,
		Content:   fmt.Sprintf("tool call: %s", call.Name),
		CreatedAt: time.Now().UTC(),
	}
}

func (s *Session) applyToolUpdates(ctx context.Context, state *runtime.SessionState, updates []tools.Update) {
	for _, update := range updates {
		if update.Message != nil {
			state.Messages = append(state.Messages, *update.Message)
			if update.Message.ToolResult != nil {
				runtime.Emit(ctx, runtime.Event{
					Type:       runtime.EventToolResult,
					SessionID:  state.SessionID,
					ToolName:   update.Message.ToolResult.Name,
					ToolCallID: update.Message.ToolResult.ToolCallID,
					Message:    update.Message.ToolResult.Content,
				})
			}
		}
		if update.Denial != nil {
			state.PermissionDenials = append(state.PermissionDenials, *update.Denial)
		}
	}
}
