package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/daewoochen/claude-code-go/internal/providers"
	"github.com/daewoochen/claude-code-go/internal/runtime"
	"github.com/daewoochen/claude-code-go/internal/session"
)

func TestSessionRunTurnPlainText(t *testing.T) {
	ctx := context.Background()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(ctx, Config{
		CWD:      t.TempDir(),
		Store:    store,
		Provider: providers.MockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "hello world", runtime.RunOptions{CWD: t.TempDir()}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if got, want := result.Result, "mock: hello world"; got != want {
		t.Fatalf("result = %q, want %q", got, want)
	}
}

func TestSessionRunTurnToolLoopAndDenial(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	store, err := session.NewStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(ctx, Config{
		CWD:            tmp,
		Store:          store,
		Provider:       providers.MockProvider{},
		PermissionMode: runtime.PermissionModeAskAsError,
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "run pwd", runtime.RunOptions{
		CWD:            tmp,
		PermissionMode: runtime.PermissionModeAskAsError,
	}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if len(result.PermissionDenials) != 1 {
		t.Fatalf("permission denials = %d, want 1", len(result.PermissionDenials))
	}
	if result.Result == "" {
		t.Fatal("expected final result after tool denial")
	}
}

func TestSessionRunTurnReadFile(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	target := filepath.Join(tmp, "note.txt")
	if err := os.WriteFile(target, []byte("hello from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(filepath.Join(tmp, "state"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(ctx, Config{
		CWD:      tmp,
		Store:    store,
		Provider: providers.MockProvider{},
	})
	if err != nil {
		t.Fatal(err)
	}

	var result runtime.RunResult
	for event := range s.RunTurn(ctx, "read note.txt", runtime.RunOptions{CWD: tmp}) {
		if event.Result != nil {
			result = *event.Result
		}
	}

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Result == "" {
		t.Fatal("expected non-empty result")
	}
}
