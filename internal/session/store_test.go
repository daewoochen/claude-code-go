package session

import (
	"context"
	"testing"
	"time"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

func TestStoreSaveLoadAndList(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := &runtime.SessionState{
		SessionID: "s1",
		Model:     "mock",
		Messages: []runtime.Message{
			{
				ID:        "m1",
				Role:      runtime.RoleUser,
				Kind:      runtime.MessageKindText,
				Content:   "hello",
				CreatedAt: time.Now().UTC(),
			},
		},
		Metadata: map[string]string{"cwd": "/tmp"},
	}
	if err := store.Append(ctx, state.SessionID, runtime.TranscriptRecord{
		Type:      "message",
		SessionID: state.SessionID,
		Message:   &state.Messages[0],
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(ctx, state); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(ctx, state.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionID != state.SessionID {
		t.Fatalf("session id = %s, want %s", loaded.SessionID, state.SessionID)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(loaded.Messages))
	}

	infos, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("infos = %d, want 1", len(infos))
	}
}
