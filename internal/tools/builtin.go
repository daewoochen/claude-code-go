package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

func Builtins() []Definition {
	return []Definition{
		echoTool(),
		readFileTool(),
		bashTool(),
	}
}

func echoTool() Definition {
	return Definition{
		Descriptor: runtime.ToolDescriptor{
			Name:            "echo",
			Description:     "Echo back a message for testing and deterministic tool loops.",
			InputSchema:     simpleSchema("message"),
			ReadOnly:        true,
			ConcurrencySafe: true,
			Source:          "builtin",
		},
		InterruptBehavior: InterruptBehaviorCancel,
		Execute: func(ctx context.Context, execCtx ExecutionContext, input map[string]any, report ProgressReporter) (runtime.ToolResult, error) {
			msg := strings.TrimSpace(asString(input["message"]))
			report("echoing message")
			return runtime.ToolResult{
				Content: msg,
				Metadata: map[string]string{
					"source": "builtin",
				},
			}, nil
		},
	}
}

func readFileTool() Definition {
	return Definition{
		Descriptor: runtime.ToolDescriptor{
			Name:            "read_file",
			Description:     "Read a UTF-8 text file from the current working directory.",
			InputSchema:     simpleSchema("path"),
			ReadOnly:        true,
			ConcurrencySafe: true,
			Source:          "builtin",
		},
		InterruptBehavior: InterruptBehaviorCancel,
		Execute: func(ctx context.Context, execCtx ExecutionContext, input map[string]any, report ProgressReporter) (runtime.ToolResult, error) {
			path := asString(input["path"])
			if path == "" {
				return runtime.ToolResult{}, fmt.Errorf("path is required")
			}
			target := path
			if !filepath.IsAbs(target) {
				target = filepath.Join(execCtx.CWD, target)
			}
			safePath, err := EnsurePathWithin(execCtx.CWD, target)
			if err != nil {
				return runtime.ToolResult{}, err
			}
			report("reading file")
			raw, err := os.ReadFile(safePath)
			if err != nil {
				return runtime.ToolResult{}, err
			}
			return runtime.ToolResult{
				Content: string(raw),
				Metadata: map[string]string{
					"path": safePath,
				},
			}, nil
		},
	}
}

func bashTool() Definition {
	return Definition{
		Descriptor: runtime.ToolDescriptor{
			Name:            "bash",
			Description:     "Execute a bash command in the current working directory.",
			InputSchema:     simpleSchema("command"),
			ReadOnly:        false,
			ConcurrencySafe: false,
			Source:          "builtin",
		},
		InterruptBehavior: InterruptBehaviorCancel,
		Execute: func(ctx context.Context, execCtx ExecutionContext, input map[string]any, report ProgressReporter) (runtime.ToolResult, error) {
			command := strings.TrimSpace(asString(input["command"]))
			if command == "" {
				return runtime.ToolResult{}, fmt.Errorf("command is required")
			}

			cmd := exec.CommandContext(ctx, "bash", "-lc", command)
			cmd.Dir = execCtx.CWD
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			report("running bash command")
			if err := cmd.Start(); err != nil {
				return runtime.ToolResult{}, err
			}
			done := make(chan error, 1)
			go func() {
				done <- cmd.Wait()
			}()
			select {
			case err := <-done:
				content := strings.TrimSpace(stdout.String())
				if strings.TrimSpace(stderr.String()) != "" {
					if content != "" {
						content += "\n"
					}
					content += strings.TrimSpace(stderr.String())
				}
				if err != nil {
					return runtime.ToolResult{
						Content: content,
						IsError: true,
						Metadata: map[string]string{
							"command": command,
						},
					}, err
				}
				return runtime.ToolResult{
					Content: content,
					Metadata: map[string]string{
						"command": command,
					},
				}, nil
			case <-ctx.Done():
				return runtime.ToolResult{}, ctx.Err()
			}
		},
	}
}

func simpleSchema(required ...string) map[string]any {
	props := make(map[string]any, len(required))
	for _, field := range required {
		props[field] = map[string]any{
			"type": "string",
		}
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties":           props,
		"required":             required,
	}
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case json.RawMessage:
		var out string
		if err := json.Unmarshal(typed, &out); err == nil {
			return out
		}
	}
	return fmt.Sprint(value)
}
