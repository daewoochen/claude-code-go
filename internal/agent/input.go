package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/daewoochen/claude-code-go/internal/runtime"
	"github.com/daewoochen/claude-code-go/internal/tools"
)

type processedInput struct {
	shouldQuery bool
	clearFirst  bool
	messages    []runtime.Message
	resultText  string
}

func (s *Session) processInput(ctx context.Context, input string) (processedInput, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return processedInput{}, nil
	}
	if strings.HasPrefix(trimmed, "/") {
		return s.processSlashCommand(ctx, trimmed)
	}
	if strings.HasPrefix(trimmed, "!") {
		return s.processLocalBash(ctx, strings.TrimSpace(strings.TrimPrefix(trimmed, "!")))
	}
	userMessage := runtime.Message{
		ID:        generateMessageID("user"),
		Role:      runtime.RoleUser,
		Kind:      runtime.MessageKindText,
		Content:   trimmed,
		CreatedAt: time.Now().UTC(),
	}
	return processedInput{
		shouldQuery: true,
		messages:    []runtime.Message{userMessage},
	}, nil
}

func (s *Session) processSlashCommand(ctx context.Context, input string) (processedInput, error) {
	parts := strings.Fields(input)
	command := strings.ToLower(strings.TrimPrefix(parts[0], "/"))
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(strings.Join(parts[1:], " "))
	}

	switch command {
	case "help":
		return assistantOnlyResult(buildHelpText()), nil
	case "tools":
		if err := s.registry.Refresh(ctx); err != nil {
			return processedInput{}, err
		}
		descriptors := s.registry.Descriptors()
		lines := make([]string, 0, len(descriptors)+1)
		lines = append(lines, "Available tools:")
		for _, descriptor := range descriptors {
			mode := "write"
			if descriptor.ReadOnly {
				mode = "read"
			}
			lines = append(lines, fmt.Sprintf("- %s [%s]: %s", descriptor.Name, mode, descriptor.Description))
		}
		return assistantOnlyResult(strings.Join(lines, "\n")), nil
	case "session":
		return assistantOnlyResult(fmt.Sprintf(
			"Session: %s\nModel: %s\nMessages: %d\nTurns: %d/%d\nPermission mode: %s",
			s.state.SessionID,
			s.state.Model,
			len(s.state.Messages),
			s.state.TurnsUsed,
			s.state.MaxTurns,
			s.state.PermissionMode,
		)), nil
	case "clear":
		return processedInput{
			shouldQuery: false,
			clearFirst:  true,
			messages: []runtime.Message{
				assistantMessage("Cleared conversation state. Session metadata and configuration were preserved."),
			},
			resultText: "Cleared conversation state. Session metadata and configuration were preserved.",
		}, nil
	case "model":
		if arg == "" {
			return assistantOnlyResult(fmt.Sprintf("Current model: %s", s.state.Model)), nil
		}
		s.state.Model = arg
		return assistantOnlyResult(fmt.Sprintf("Model updated to %s", arg)), nil
	case "permission":
		if arg == "" {
			return assistantOnlyResult(fmt.Sprintf("Current permission mode: %s", s.state.PermissionMode)), nil
		}
		mode := runtime.PermissionMode(arg)
		switch mode {
		case runtime.PermissionModeAllowAll, runtime.PermissionModeDenyAll, runtime.PermissionModeAskAsError:
			s.state.PermissionMode = mode
			return assistantOnlyResult(fmt.Sprintf("Permission mode updated to %s", mode)), nil
		default:
			return assistantOnlyResult("Unknown permission mode. Use allow_all, deny_all, or ask_as_error."), nil
		}
	default:
		return assistantOnlyResult(buildHelpText()), nil
	}
}

func (s *Session) processLocalBash(ctx context.Context, command string) (processedInput, error) {
	if command == "" {
		return assistantOnlyResult("Local bash command is empty."), nil
	}
	executor := tools.Executor{
		Registry: s.registry,
		Policy: tools.Policy{
			Mode: s.state.PermissionMode,
		},
	}
	call := runtime.ToolCall{
		ID:              generateMessageID("tool"),
		Name:            "bash",
		Input:           map[string]any{"command": command},
		ReadOnly:        false,
		ConcurrencySafe: false,
	}
	updates, err := executor.ExecuteBatch(ctx, tools.ExecutionContext{
		SessionID: s.state.SessionID,
		CWD:       s.state.Metadata["cwd"],
	}, []runtime.ToolCall{call})
	if err != nil {
		return processedInput{}, err
	}

	messages := []runtime.Message{
		{
			ID:        generateMessageID("user"),
			Role:      runtime.RoleUser,
			Kind:      runtime.MessageKindText,
			Content:   "!" + command,
			CreatedAt: time.Now().UTC(),
		},
	}
	resultText := ""
	for _, update := range updates {
		if update.Denial != nil {
			s.state.PermissionDenials = append(s.state.PermissionDenials, *update.Denial)
		}
		if update.Message != nil {
			messages = append(messages, *update.Message)
			if update.Message.ToolResult != nil {
				resultText = update.Message.ToolResult.Content
			}
		}
	}
	if resultText == "" {
		resultText = "Local bash command finished."
	}
	messages = append(messages, assistantMessage(resultText))
	return processedInput{
		shouldQuery: false,
		messages:    messages,
		resultText:  resultText,
	}, nil
}

func assistantOnlyResult(text string) processedInput {
	return processedInput{
		shouldQuery: false,
		messages:    []runtime.Message{assistantMessage(text)},
		resultText:  text,
	}
}

func assistantMessage(text string) runtime.Message {
	return runtime.Message{
		ID:        generateMessageID("assistant"),
		Role:      runtime.RoleAssistant,
		Kind:      runtime.MessageKindText,
		Content:   text,
		CreatedAt: time.Now().UTC(),
	}
}

func buildHelpText() string {
	return strings.Join([]string{
		"Available slash commands:",
		"- /help: show command help",
		"- /tools: list registered tools",
		"- /session: show session details",
		"- /clear: clear conversation history",
		"- /model <name>: switch model for this session",
		"- /permission <mode>: set permission mode",
		"- !<command>: run a local bash command without going through the model",
	}, "\n")
}
