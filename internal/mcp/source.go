package mcp

import (
	"context"
	"fmt"

	"github.com/daewoochen/claude-code-go/internal/runtime"
	"github.com/daewoochen/claude-code-go/internal/tools"
)

func (m *Manager) RefreshTools(ctx context.Context) ([]tools.Definition, error) {
	if m == nil {
		return nil, nil
	}
	if err := m.Refresh(ctx); err != nil {
		return nil, err
	}
	var defs []tools.Definition
	for _, server := range m.config.Servers {
		switch server.Transport {
		case "", "stdio":
			stdioDefs, err := definitionsFromStdioServer(ctx, server)
			if err != nil {
				return nil, fmt.Errorf("load MCP tools for server %s: %w", server.Name, err)
			}
			defs = append(defs, stdioDefs...)
		}
		for _, staticTool := range server.StaticTools {
			serverName := server.Name
			toolConfig := staticTool
			defs = append(defs, tools.Definition{
				Descriptor: runtime.ToolDescriptor{
					Name:            fmt.Sprintf("mcp__%s__%s", serverName, toolConfig.Name),
					Description:     toolConfig.Description,
					InputSchema:     toolConfig.InputSchema,
					ReadOnly:        true,
					ConcurrencySafe: true,
					Source:          "mcp",
					Metadata: map[string]string{
						"server": serverName,
					},
				},
				InterruptBehavior: tools.InterruptBehaviorCancel,
				Execute: func(ctx context.Context, execCtx tools.ExecutionContext, input map[string]any, report tools.ProgressReporter) (runtime.ToolResult, error) {
					_ = ctx
					_ = execCtx
					_ = input
					report("resolved static MCP tool result")
					return runtime.ToolResult{
						Content: toolConfig.Result,
						Metadata: map[string]string{
							"server": serverName,
						},
					}, nil
				},
			})
		}
	}
	return defs, nil
}

func definitionsFromStdioServer(ctx context.Context, server ServerConfig) ([]tools.Definition, error) {
	client, err := newStdioClient(ctx, server)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	if err := client.Initialize(ctx); err != nil {
		return nil, err
	}
	listed, err := client.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	defs := make([]tools.Definition, 0, len(listed))
	for _, listedTool := range listed {
		serverName := server.Name
		toolName := listedTool.Name
		schema := listedTool.InputSchema
		if len(schema) == 0 {
			schema = listedTool.InputSchema2
		}
		readOnly := listedTool.Annotations.ReadOnlyHint
		defs = append(defs, tools.Definition{
			Descriptor: runtime.ToolDescriptor{
				Name:            fmt.Sprintf("mcp__%s__%s", serverName, toolName),
				Description:     listedTool.Description,
				InputSchema:     schema,
				ReadOnly:        readOnly,
				ConcurrencySafe: readOnly,
				Source:          "mcp",
				Metadata: map[string]string{
					"server":    serverName,
					"transport": "stdio",
				},
			},
			InterruptBehavior: tools.InterruptBehaviorCancel,
			Execute: func(ctx context.Context, execCtx tools.ExecutionContext, input map[string]any, report tools.ProgressReporter) (runtime.ToolResult, error) {
				_ = execCtx
				report("connecting to MCP stdio server")
				client, err := newStdioClient(ctx, server)
				if err != nil {
					return runtime.ToolResult{}, err
				}
				defer func() { _ = client.Close() }()
				if err := client.Initialize(ctx); err != nil {
					return runtime.ToolResult{}, err
				}
				report("calling MCP tool")
				result, err := client.CallTool(ctx, toolName, input)
				if err != nil {
					return runtime.ToolResult{}, err
				}
				if result.Metadata == nil {
					result.Metadata = map[string]string{}
				}
				result.Metadata["server"] = serverName
				result.Metadata["transport"] = "stdio"
				return result, nil
			},
		})
	}
	return defs, nil
}
