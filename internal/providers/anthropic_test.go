package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

func TestAnthropicProviderStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !strings.Contains(string(body), `"stream":true`) {
			t.Fatalf("expected streaming request, got %s", string(body))
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Hel\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"echo\",\"input\":{}}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"message\\\":\\\"world\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":9}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	provider := AnthropicProvider{
		APIKey:  "test-key",
		BaseURL: server.URL,
	}
	var deltas []string
	response, err := provider.Generate(context.Background(), GenerateRequest{
		Model:           "claude-test",
		SystemPrompt:    "system",
		Messages:        []runtime.Message{{Role: runtime.RoleUser, Kind: runtime.MessageKindText, Content: "say hello"}},
		MaxOutputTokens: 256,
		OnAssistantDelta: func(delta string) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(deltas, ""), "Hello"; got != want {
		t.Fatalf("deltas = %q, want %q", got, want)
	}
	if got, want := response.AssistantText, "Hello"; got != want {
		t.Fatalf("assistant text = %q, want %q", got, want)
	}
	if !response.StreamedText {
		t.Fatal("expected streamed text response")
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(response.ToolCalls))
	}
	if response.ToolCalls[0].Input["message"] != "world" {
		t.Fatalf("tool input = %#v, want message=world", response.ToolCalls[0].Input)
	}
	if response.StopReason != "tool_use" {
		t.Fatalf("stop reason = %q, want tool_use", response.StopReason)
	}
}
