package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/daewoochen/claude-code-go/internal/agent"
	"github.com/daewoochen/claude-code-go/internal/mcp"
	"github.com/daewoochen/claude-code-go/internal/providers"
	"github.com/daewoochen/claude-code-go/internal/runtime"
	"github.com/daewoochen/claude-code-go/internal/session"
)

type App struct {
	stdout io.Writer
	stderr io.Writer
}

func NewApp(stdout, stderr io.Writer) *App {
	return &App{stdout: stdout, stderr: stderr}
}

func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.printRootHelp()
		return 1
	}
	switch args[0] {
	case "run":
		return a.runCommand(ctx, args[1:], "text")
	case "print":
		return a.runCommand(ctx, args[1:], "stream-json")
	case "resume":
		return a.resumeCommand(ctx, args[1:])
	case "sessions":
		return a.sessionsCommand(ctx, args[1:])
	case "mcp":
		return a.mcpCommand(ctx, args[1:])
	default:
		fmt.Fprintf(a.stderr, "unknown command: %s\n", args[0])
		a.printRootHelp()
		return 1
	}
}

func (a *App) runCommand(ctx context.Context, args []string, defaultOutput string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	cwd := fs.String("cwd", mustGetwd(), "working directory")
	model := fs.String("model", "", "model name")
	maxTurns := fs.Int("max-turns", 8, "max agent turns")
	sessionID := fs.String("session-id", "", "session id")
	appendSystemPrompt := fs.String("append-system-prompt", "", "append to system prompt")
	output := fs.String("output", defaultOutput, "text|json|stream-json")
	stateDir := fs.String("state-dir", "", "state directory")
	providerName := fs.String("provider", defaultProviderName(), "mock|anthropic")
	mcpConfig := fs.String("mcp-config", "", "path to MCP config")
	strictMCP := fs.Bool("strict-mcp-config", false, "strict MCP config")
	permissionMode := fs.String("permission-mode", string(runtime.PermissionModeAskAsError), "allow_all|deny_all|ask_as_error")
	fallbackModel := fs.String("fallback-model", "", "fallback model")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		raw, _ := io.ReadAll(os.Stdin)
		prompt = strings.TrimSpace(string(raw))
	}
	if prompt == "" {
		fmt.Fprintln(a.stderr, "prompt is required")
		return 1
	}
	store, err := session.NewStore(*stateDir)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	manager, err := mcp.NewManager(*mcpConfig)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	s, err := agent.NewSession(ctx, agent.Config{
		SessionID:          *sessionID,
		CWD:                *cwd,
		Model:              *model,
		MaxTurns:           *maxTurns,
		PermissionMode:     runtime.PermissionMode(*permissionMode),
		Store:              store,
		Provider:           selectProvider(*providerName),
		MCP:                manager,
		AppendSystemPrompt: *appendSystemPrompt,
		FallbackModel:      *fallbackModel,
	})
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	return a.consumeEvents(s.RunTurn(ctx, prompt, runtime.RunOptions{
		CWD:                *cwd,
		Model:              *model,
		MaxTurns:           *maxTurns,
		SessionID:          *sessionID,
		AppendSystemPrompt: *appendSystemPrompt,
		StrictMCPConfig:    *strictMCP,
		OutputMode:         *output,
		PermissionMode:     runtime.PermissionMode(*permissionMode),
		FallbackModel:      *fallbackModel,
	}), *output)
}

func (a *App) resumeCommand(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	sessionID := fs.String("session-id", "", "session id")
	stateDir := fs.String("state-dir", "", "state directory")
	cwd := fs.String("cwd", mustGetwd(), "working directory")
	providerName := fs.String("provider", defaultProviderName(), "mock|anthropic")
	model := fs.String("model", "", "model name")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *sessionID == "" {
		fmt.Fprintln(a.stderr, "--session-id is required")
		return 1
	}
	store, err := session.NewStore(*stateDir)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	s, err := agent.ResumeSession(ctx, agent.Config{
		CWD:      *cwd,
		Model:    *model,
		Store:    store,
		Provider: selectProvider(*providerName),
	}, *sessionID)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		state := s.State()
		payload, _ := json.MarshalIndent(state, "", "  ")
		fmt.Fprintln(a.stdout, string(payload))
		return 0
	}
	return a.consumeEvents(s.RunTurn(ctx, prompt, runtime.RunOptions{
		CWD:       *cwd,
		Model:     *model,
		SessionID: *sessionID,
	}), "stream-json")
}

func (a *App) sessionsCommand(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(a.stderr, "usage: sessions list [--state-dir DIR]")
		return 1
	}
	fs := flag.NewFlagSet("sessions list", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	stateDir := fs.String("state-dir", "", "state directory")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	store, err := session.NewStore(*stateDir)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	infos, err := store.ListSessions(ctx)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	payload, _ := json.MarshalIndent(infos, "", "  ")
	fmt.Fprintln(a.stdout, string(payload))
	return 0
}

func (a *App) mcpCommand(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(a.stderr, "usage: mcp check --mcp-config path")
		return 1
	}
	fs := flag.NewFlagSet("mcp check", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	path := fs.String("mcp-config", "", "path to MCP config")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	manager, err := mcp.NewManager(*path)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	statuses, err := manager.Check(ctx)
	if err != nil {
		fmt.Fprintln(a.stderr, err)
		return 1
	}
	payload, _ := json.MarshalIndent(statuses, "", "  ")
	fmt.Fprintln(a.stdout, string(payload))
	return 0
}

func (a *App) consumeEvents(events <-chan runtime.Event, mode string) int {
	var final runtime.RunResult
	for event := range events {
		switch mode {
		case "stream-json":
			raw, _ := json.Marshal(event)
			fmt.Fprintln(a.stdout, string(raw))
		case "json":
			if event.Result != nil {
				final = *event.Result
			}
		default:
			switch event.Type {
			case runtime.EventAssistantDelta:
				fmt.Fprint(a.stdout, event.Delta)
			case runtime.EventAssistant:
				if event.Message != "" && !strings.HasSuffix(event.Message, "\n") {
					fmt.Fprintln(a.stdout)
				}
			case runtime.EventToolProgress, runtime.EventSystem:
				fmt.Fprintf(a.stderr, "[%s] %s\n", event.Type, event.Message)
			case runtime.EventResult:
				if event.Result != nil {
					final = *event.Result
				}
			}
		}
	}
	if mode == "json" {
		raw, _ := json.MarshalIndent(final, "", "  ")
		fmt.Fprintln(a.stdout, string(raw))
	}
	if final.Error != "" {
		fmt.Fprintln(a.stderr, final.Error)
		return 1
	}
	return 0
}

func (a *App) printRootHelp() {
	fmt.Fprintln(a.stderr, "ccgo commands: run, print, resume, sessions list, mcp check")
}

func selectProvider(name string) providers.ChatModelProvider {
	switch name {
	case "anthropic":
		return providers.AnthropicProvider{
			APIKey:  os.Getenv("ANTHROPIC_API_KEY"),
			BaseURL: os.Getenv("ANTHROPIC_BASE_URL"),
		}
	default:
		return providers.MockProvider{}
	}
}

func defaultProviderName() string {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "anthropic"
	}
	return "mock"
}

func mustGetwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
