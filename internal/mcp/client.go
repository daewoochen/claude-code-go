package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

var supportedProtocolVersions = []string{
	"2025-03-26",
	"2024-11-05",
	"2024-10-07",
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeResult struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type listedTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema,omitempty"`
	InputSchema2 map[string]any `json:"input_schema,omitempty"`
	Annotations  struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations,omitempty"`
}

type listToolsResult struct {
	Tools []listedTool `json:"tools"`
}

type toolCallResult struct {
	Content           []toolContentBlock `json:"content,omitempty"`
	StructuredContent any                `json:"structuredContent,omitempty"`
	IsError           bool               `json:"isError,omitempty"`
}

type toolContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type stdioClient struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	closeOnce sync.Once
	writeMu   sync.Mutex
	nextID    int64
}

func newStdioClient(ctx context.Context, cfg ServerConfig) (*stdioClient, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("missing command for stdio transport")
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Env = mergeEnv(cfg.Env)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start stdio server %s: %w", cfg.Name, err)
	}
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
	}()
	return &stdioClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}, nil
}

func (c *stdioClient) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.cmd == nil || c.cmd.Process == nil {
			return
		}
		if err := c.cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				closeErr = err
			}
		}
	})
	return closeErr
}

func (c *stdioClient) Initialize(ctx context.Context) error {
	var lastErr error
	for _, version := range supportedProtocolVersions {
		resp, err := c.request(ctx, "initialize", map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "claude-code-go",
				"version": "0.1.0",
			},
		})
		if err != nil {
			lastErr = err
			if !looksLikeProtocolMismatch(err) {
				return err
			}
			continue
		}
		var result initializeResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return fmt.Errorf("decode initialize result: %w", err)
		}
		return c.notify("notifications/initialized", map[string]any{})
	}
	if lastErr == nil {
		lastErr = errors.New("initialize failed")
	}
	return lastErr
}

func (c *stdioClient) ListTools(ctx context.Context) ([]listedTool, error) {
	resp, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result listToolsResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("decode tools/list result: %w", err)
	}
	return result.Tools, nil
}

func (c *stdioClient) CallTool(ctx context.Context, name string, arguments map[string]any) (runtime.ToolResult, error) {
	resp, err := c.request(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return runtime.ToolResult{}, err
	}
	var result toolCallResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return runtime.ToolResult{}, fmt.Errorf("decode tools/call result: %w", err)
	}
	content, metadata := flattenToolResult(result)
	return runtime.ToolResult{
		Content:  content,
		IsError:  result.IsError,
		Metadata: metadata,
	}, nil
}

func (c *stdioClient) notify(method string, params any) error {
	message := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	return c.write(message)
}

func (c *stdioClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextRequestID()
	message := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.write(message); err != nil {
		return nil, err
	}
	idBytes := []byte(strconv.FormatInt(id, 10))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := readFramedMessage(c.stdout)
		if err != nil {
			return nil, err
		}
		var envelope rpcMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, fmt.Errorf("decode rpc message: %w", err)
		}
		if len(envelope.Method) > 0 && len(envelope.ID) > 0 {
			_ = c.respondNotImplemented(envelope.ID, envelope.Method)
			continue
		}
		if len(envelope.ID) == 0 {
			continue
		}
		if !bytes.Equal(bytes.TrimSpace(envelope.ID), idBytes) {
			continue
		}
		if envelope.Error != nil {
			return nil, fmt.Errorf("mcp %s: %s", method, envelope.Error.Message)
		}
		return envelope.Result, nil
	}
}

func (c *stdioClient) respondNotImplemented(id json.RawMessage, method string) error {
	return c.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(bytes.TrimSpace(id)),
		"error": map[string]any{
			"code":    -32601,
			"message": fmt.Sprintf("client does not implement %s", method),
		},
	})
}

func (c *stdioClient) nextRequestID() int64 {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.nextID++
	return c.nextID
}

func (c *stdioClient) write(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal rpc message: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return fmt.Errorf("write rpc header: %w", err)
	}
	if _, err := c.stdin.Write(payload); err != nil {
		return fmt.Errorf("write rpc body: %w", err)
	}
	return nil
}

func readFramedMessage(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if !strings.HasPrefix(strings.ToLower(line), "content-length:") {
			continue
		}
		value := strings.TrimSpace(line[len("content-length:"):])
		length, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("invalid content-length %q: %w", value, err)
		}
		contentLength = length
	}
	if contentLength < 0 {
		return nil, errors.New("missing content-length header")
	}
	payload := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func flattenToolResult(result toolCallResult) (string, map[string]string) {
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		default:
			raw, _ := json.Marshal(block)
			parts = append(parts, string(raw))
		}
	}
	if len(parts) == 0 && result.StructuredContent != nil {
		raw, _ := json.MarshalIndent(result.StructuredContent, "", "  ")
		parts = append(parts, string(raw))
	}
	metadata := map[string]string{}
	if result.IsError {
		metadata["mcp_result"] = "error"
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), metadata
}

func mergeEnv(overrides map[string]string) []string {
	env := os.Environ()
	if len(overrides) == 0 {
		return env
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func looksLikeProtocolMismatch(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "protocol") || strings.Contains(text, "version")
}
