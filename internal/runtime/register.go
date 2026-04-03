package runtime

import "github.com/cloudwego/eino/schema"

func init() {
	schema.RegisterName[*SessionState]("ccgo_session_state")
	schema.RegisterName[*Message]("ccgo_message")
	schema.RegisterName[*ToolCall]("ccgo_tool_call")
	schema.RegisterName[*ToolResult]("ccgo_tool_result")
}
