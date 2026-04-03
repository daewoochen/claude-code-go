package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/daewoochen/claude-code-go/internal/tools"
)

func TestManagerRefreshToolsStdio(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configPath := writeTestConfig(t, Config{
		Servers: []ServerConfig{
			{
				Name:      "demo",
				Transport: "stdio",
				Command:   executable,
				Args:      []string{"-test.run=TestMCPHelperProcess"},
				Env: map[string]string{
					"CCGO_TEST_MCP_HELPER": "1",
				},
			},
		},
	})

	manager, err := NewManager(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := manager.RefreshTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("definitions = %d, want 1", len(defs))
	}
	def := defs[0]
	if got, want := def.Descriptor.Name, "mcp__demo__hello"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
	if !def.Descriptor.ReadOnly {
		t.Fatal("expected MCP tool to be marked read-only from annotations")
	}

	result, err := def.Execute(context.Background(), tools.ExecutionContext{
		SessionID: "s1",
		CWD:       t.TempDir(),
	}, map[string]any{
		"name": "gopher",
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Content, "hello, gopher"; got != want {
		t.Fatalf("tool result = %q, want %q", got, want)
	}
	if got, want := result.Metadata["server"], "demo"; got != want {
		t.Fatalf("result metadata server = %q, want %q", got, want)
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("CCGO_TEST_MCP_HELPER") != "1" {
		return
	}
	if err := runTestMCPServer(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(2)
	}
	os.Exit(0)
}

func runTestMCPServer(stdin *os.File, stdout *os.File) error {
	reader := bufio.NewReader(stdin)
	writer := bufio.NewWriter(stdout)
	for {
		payload, err := readFramedMessage(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var message rpcMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			return err
		}
		switch message.Method {
		case "initialize":
			if err := writeRPCMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(message.ID),
				"result": map[string]any{
					"protocolVersion": "2025-03-26",
					"capabilities": map[string]any{
						"tools": map[string]any{},
					},
					"serverInfo": map[string]any{
						"name":    "test-mcp",
						"version": "0.0.1",
					},
				},
			}); err != nil {
				return err
			}
		case "notifications/initialized":
			continue
		case "tools/list":
			if err := writeRPCMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(message.ID),
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "hello",
							"description": "Say hello from the MCP test server.",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"name": map[string]any{"type": "string"},
								},
							},
							"annotations": map[string]any{
								"readOnlyHint": true,
							},
						},
					},
				},
			}); err != nil {
				return err
			}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(message.Params, &params); err != nil {
				return err
			}
			if err := writeRPCMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(message.ID),
				"result": map[string]any{
					"content": []map[string]any{
						{
							"type": "text",
							"text": fmt.Sprintf("hello, %v", params.Arguments["name"]),
						},
					},
				},
			}); err != nil {
				return err
			}
		default:
			if len(message.ID) == 0 {
				continue
			}
			if err := writeRPCMessage(writer, map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(message.ID),
				"error": map[string]any{
					"code":    -32601,
					"message": "method not found",
				},
			}); err != nil {
				return err
			}
		}
	}
}

func writeRPCMessage(writer *bufio.Writer, message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return writer.Flush()
}

func writeTestConfig(t *testing.T, cfg Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
