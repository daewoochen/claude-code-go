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
