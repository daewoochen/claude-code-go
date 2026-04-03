package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/daewoochen/claude-code-go/internal/runtime"
)

const (
	transcriptFileName = "transcript.jsonl"
	snapshotFileName   = "snapshot.json"
	metadataFileName   = "metadata.json"
)

type Metadata struct {
	SessionID  string    `json:"session_id"`
	UpdatedAt  time.Time `json:"updated_at"`
	MessageCnt int       `json:"message_count"`
	Model      string    `json:"model,omitempty"`
	CWD        string    `json:"cwd,omitempty"`
}

type SessionInfo struct {
	Metadata Metadata `json:"metadata"`
}

type Store struct {
	baseDir string
}

func DefaultBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ccgo"
	}
	return filepath.Join(home, ".ccgo")
}

func NewStore(baseDir string) (*Store, error) {
	if baseDir == "" {
		baseDir = DefaultBaseDir()
	}
	baseDir = filepath.Clean(baseDir)
	if err := os.MkdirAll(filepath.Join(baseDir, "sessions"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "checkpoints"), 0o755); err != nil {
		return nil, err
	}
	return &Store{baseDir: baseDir}, nil
}

func (s *Store) BaseDir() string {
	return s.baseDir
}

func (s *Store) Append(ctx context.Context, sessionID string, records ...runtime.TranscriptRecord) error {
	_ = ctx
	if sessionID == "" {
		return errors.New("session id is empty")
	}
	if len(records) == 0 {
		return nil
	}
	path := filepath.Join(s.sessionDir(sessionID), transcriptFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	for _, record := range records {
		if record.Timestamp.IsZero() {
			record.Timestamp = time.Now().UTC()
		}
		if err := enc.Encode(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveSnapshot(ctx context.Context, state *runtime.SessionState) error {
	_ = ctx
	if state == nil {
		return nil
	}
	if err := os.MkdirAll(s.sessionDir(state.SessionID), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.sessionDir(state.SessionID), snapshotFileName), payload, 0o644); err != nil {
		return err
	}
	return s.saveMetadata(state)
}

func (s *Store) saveMetadata(state *runtime.SessionState) error {
	meta := Metadata{
		SessionID:  state.SessionID,
		UpdatedAt:  time.Now().UTC(),
		MessageCnt: len(state.Messages),
		Model:      state.Model,
	}
	if state.Metadata != nil {
		meta.CWD = state.Metadata["cwd"]
	}
	payload, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.sessionDir(state.SessionID), metadataFileName), payload, 0o644)
}

func (s *Store) Load(ctx context.Context, sessionID string) (*runtime.SessionState, error) {
	_ = ctx
	if sessionID == "" {
		return nil, errors.New("session id is empty")
	}
	snapshotPath := filepath.Join(s.sessionDir(sessionID), snapshotFileName)
	if raw, err := os.ReadFile(snapshotPath); err == nil {
		var state runtime.SessionState
		if err := json.Unmarshal(raw, &state); err == nil {
			return &state, nil
		}
	}
	return s.rebuildFromTranscript(sessionID)
}

func (s *Store) ResumeFromCheckpoint(ctx context.Context, sessionID string) (*runtime.SessionState, error) {
	return s.Load(ctx, sessionID)
}

func (s *Store) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	_ = ctx
	root := filepath.Join(s.baseDir, "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	infos := make([]SessionInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name(), metadataFileName))
		if err != nil {
			continue
		}
		var meta Metadata
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		infos = append(infos, SessionInfo{Metadata: meta})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Metadata.UpdatedAt.After(infos[j].Metadata.UpdatedAt)
	})
	return infos, nil
}

func (s *Store) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	_ = ctx
	path := filepath.Join(s.baseDir, "checkpoints", filepath.Clean(checkPointID)+".bin")
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return raw, true, nil
}

func (s *Store) Set(ctx context.Context, checkPointID string, checkpoint []byte) error {
	_ = ctx
	path := filepath.Join(s.baseDir, "checkpoints", filepath.Clean(checkPointID)+".bin")
	return os.WriteFile(path, checkpoint, 0o644)
}

func (s *Store) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, "sessions", sessionID)
}

func (s *Store) rebuildFromTranscript(sessionID string) (*runtime.SessionState, error) {
	path := filepath.Join(s.sessionDir(sessionID), transcriptFileName)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	state := &runtime.SessionState{
		SessionID:        sessionID,
		ReadFileCache:    map[string]string{},
		ToolResultBudget: 64 * 1024,
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record runtime.TranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("parse transcript: %w", err)
		}
		if record.State != nil {
			state = record.State
			continue
		}
		if record.Message != nil {
			state.Messages = append(state.Messages, *record.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return state, nil
}
