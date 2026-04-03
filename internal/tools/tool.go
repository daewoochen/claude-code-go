package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

type InterruptBehavior string

const (
	InterruptBehaviorCancel InterruptBehavior = "cancel"
	InterruptBehaviorBlock  InterruptBehavior = "block"
)

type PermissionDecision string

const (
	PermissionDecisionAllow PermissionDecision = "allow"
	PermissionDecisionDeny  PermissionDecision = "deny"
	PermissionDecisionAsk   PermissionDecision = "ask"
)

type ProgressReporter func(message string)

type ExecutionContext struct {
	SessionID string
	CWD       string
}

type ExecuteFunc func(ctx context.Context, execCtx ExecutionContext, input map[string]any, report ProgressReporter) (runtime.ToolResult, error)

type Definition struct {
	Descriptor         runtime.ToolDescriptor
	InterruptBehavior  InterruptBehavior
	PermissionOverride func(input map[string]any) PermissionDecision
	Execute            ExecuteFunc
}

type DynamicSource interface {
	Refresh(ctx context.Context) ([]Definition, error)
}

type Registry struct {
	mu            sync.RWMutex
	tools         map[string]Definition
	dynamicSource []DynamicSource
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Definition),
	}
}

func (r *Registry) Register(tool Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Descriptor.Name] = tool
}

func (r *Registry) RegisterAll(tools ...Definition) {
	for _, tool := range tools {
		r.Register(tool)
	}
}

func (r *Registry) AddDynamicSource(source DynamicSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dynamicSource = append(r.dynamicSource, source)
}

func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, source := range r.dynamicSource {
		tools, err := source.Refresh(ctx)
		if err != nil {
			return err
		}
		for _, tool := range tools {
			r.tools[tool.Descriptor.Name] = tool
		}
	}
	return nil
}

func (r *Registry) Lookup(name string) (Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *Registry) Descriptors() []runtime.ToolDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptors := make([]runtime.ToolDescriptor, 0, len(r.tools))
	for _, tool := range r.tools {
		descriptors = append(descriptors, tool.Descriptor)
	}
	sort.Slice(descriptors, func(i, j int) bool {
		return descriptors[i].Name < descriptors[j].Name
	})
	return descriptors
}

type Policy struct {
	Mode      runtime.PermissionMode
	AllowList map[string]bool
	DenyList  map[string]bool
}

func (p Policy) Decide(tool Definition, input map[string]any) PermissionDecision {
	if tool.PermissionOverride != nil {
		if decision := tool.PermissionOverride(input); decision != "" {
			return decision
		}
	}
	if p.DenyList != nil && p.DenyList[tool.Descriptor.Name] {
		return PermissionDecisionDeny
	}
	if p.AllowList != nil && p.AllowList[tool.Descriptor.Name] {
		return PermissionDecisionAllow
	}
	switch p.Mode {
	case runtime.PermissionModeDenyAll:
		return PermissionDecisionDeny
	case runtime.PermissionModeAskAsError:
		return PermissionDecisionAsk
	default:
		return PermissionDecisionAllow
	}
}

type Executor struct {
	Registry *Registry
	Policy   Policy
}

type Update struct {
	Message *runtime.Message
	Denial  *runtime.PermissionDenial
}

func (e Executor) ExecuteBatch(ctx context.Context, execCtx ExecutionContext, calls []runtime.ToolCall) ([]Update, error) {
	if e.Registry == nil {
		return nil, errors.New("tool registry is nil")
	}
	var updates []Update
	for _, batch := range partitionCalls(calls, e.Registry) {
		if batch.concurrent {
			concurrentUpdates, err := e.executeConcurrent(ctx, execCtx, batch.calls)
			if err != nil {
				return nil, err
			}
			updates = append(updates, concurrentUpdates...)
			continue
		}
		for _, call := range batch.calls {
			callUpdates, err := e.executeOne(ctx, execCtx, call)
			if err != nil {
				return nil, err
			}
			updates = append(updates, callUpdates...)
		}
	}
	return updates, nil
}

type batch struct {
	concurrent bool
	calls      []runtime.ToolCall
}

func partitionCalls(calls []runtime.ToolCall, registry *Registry) []batch {
	batches := make([]batch, 0, len(calls))
	for _, call := range calls {
		tool, ok := registry.Lookup(call.Name)
		concurrent := ok && tool.Descriptor.ConcurrencySafe
		if len(batches) > 0 && batches[len(batches)-1].concurrent && concurrent {
			batches[len(batches)-1].calls = append(batches[len(batches)-1].calls, call)
			continue
		}
		batches = append(batches, batch{
			concurrent: concurrent,
			calls:      []runtime.ToolCall{call},
		})
	}
	return batches
}

func (e Executor) executeConcurrent(ctx context.Context, execCtx ExecutionContext, calls []runtime.ToolCall) ([]Update, error) {
	type result struct {
		index   int
		updates []Update
		err     error
	}
	results := make(chan result, len(calls))
	for i, call := range calls {
		go func(index int, toolCall runtime.ToolCall) {
			updates, err := e.executeOne(ctx, execCtx, toolCall)
			results <- result{index: index, updates: updates, err: err}
		}(i, call)
	}
	ordered := make([][]Update, len(calls))
	for range calls {
		result := <-results
		if result.err != nil {
			return nil, result.err
		}
		ordered[result.index] = result.updates
	}
	var merged []Update
	for _, updates := range ordered {
		merged = append(merged, updates...)
	}
	return merged, nil
}

func (e Executor) executeOne(ctx context.Context, execCtx ExecutionContext, call runtime.ToolCall) ([]Update, error) {
	tool, ok := e.Registry.Lookup(call.Name)
	if !ok {
		result := runtime.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("unknown tool: %s", call.Name),
			IsError:    true,
		}
		return []Update{{Message: toolResultMessage(result)}}, nil
	}

	decision := e.Policy.Decide(tool, call.Input)
	switch decision {
	case PermissionDecisionDeny, PermissionDecisionAsk:
		result := runtime.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("permission denied for tool %s", call.Name),
			IsError:    true,
		}
		reason := "deny"
		if decision == PermissionDecisionAsk {
			reason = "ask_as_error"
		}
		return []Update{
			{
				Message: toolResultMessage(result),
				Denial: &runtime.PermissionDenial{
					ToolName:   call.Name,
					ToolCallID: call.ID,
					Input:      call.Input,
					Reason:     reason,
				},
			},
		}, nil
	}

	runtime.Emit(ctx, runtime.Event{
		Type:       runtime.EventToolProgress,
		SessionID:  execCtx.SessionID,
		ToolName:   call.Name,
		ToolCallID: call.ID,
		Message:    "starting",
		At:         time.Now().UTC(),
	})

	report := func(message string) {
		runtime.Emit(ctx, runtime.Event{
			Type:       runtime.EventToolProgress,
			SessionID:  execCtx.SessionID,
			ToolName:   call.Name,
			ToolCallID: call.ID,
			Message:    message,
			At:         time.Now().UTC(),
		})
	}

	result, err := tool.Execute(ctx, execCtx, call.Input, report)
	if err != nil {
		result = runtime.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    err.Error(),
			IsError:    true,
		}
	}
	result.ToolCallID = call.ID
	result.Name = call.Name
	return []Update{{Message: toolResultMessage(result)}}, nil
}

func toolResultMessage(result runtime.ToolResult) *runtime.Message {
	return &runtime.Message{
		ID:         newMessageID("tool"),
		Role:       runtime.RoleTool,
		Kind:       runtime.MessageKindToolResult,
		ToolResult: &result,
		Content:    result.Content,
		CreatedAt:  time.Now().UTC(),
	}
}

func newMessageID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func EnsurePathWithin(baseDir, target string) (string, error) {
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("path %s escapes cwd %s", targetAbs, baseAbs)
	}
	return targetAbs, nil
}
