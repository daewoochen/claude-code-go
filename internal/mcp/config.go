package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type StaticToolConfig struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Result      string            `json:"result"`
	InputSchema map[string]any    `json:"input_schema,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type ServerConfig struct {
	Name        string             `json:"name"`
	Transport   string             `json:"transport"`
	Command     string             `json:"command,omitempty"`
	Args        []string           `json:"args,omitempty"`
	Env         map[string]string  `json:"env,omitempty"`
	URL         string             `json:"url,omitempty"`
	StaticTools []StaticToolConfig `json:"static_tools,omitempty"`
}

type Config struct {
	Servers []ServerConfig `json:"servers"`
}

type ServerStatus struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Valid     bool   `json:"valid"`
	Error     string `json:"error,omitempty"`
}

type Manager struct {
	path   string
	config Config
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse mcp config: %w", err)
	}
	for i := range cfg.Servers {
		if cfg.Servers[i].Name == "" {
			return Config{}, fmt.Errorf("server %d missing name", i)
		}
		if cfg.Servers[i].Transport == "" {
			cfg.Servers[i].Transport = "stdio"
		}
	}
	return cfg, nil
}

func NewManager(path string) (*Manager, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return &Manager{path: path, config: cfg}, nil
}

func (m *Manager) Config() Config {
	return m.config
}

func (m *Manager) Refresh(context.Context) error {
	if m == nil || m.path == "" {
		return nil
	}
	cfg, err := LoadConfig(m.path)
	if err != nil {
		return err
	}
	m.config = cfg
	return nil
}

func (m *Manager) Check(ctx context.Context) ([]ServerStatus, error) {
	if err := m.Refresh(ctx); err != nil {
		return nil, err
	}
	statuses := make([]ServerStatus, 0, len(m.config.Servers))
	for _, server := range m.config.Servers {
		status := ServerStatus{
			Name:      server.Name,
			Transport: server.Transport,
			Valid:     true,
		}
		switch server.Transport {
		case "stdio":
			if server.Command == "" {
				status.Valid = false
				status.Error = "missing command for stdio transport"
			}
		case "http", "sse", "ws":
			if server.URL == "" {
				status.Valid = false
				status.Error = "missing url for network transport"
			}
		default:
			status.Valid = false
			status.Error = "unsupported transport"
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}
