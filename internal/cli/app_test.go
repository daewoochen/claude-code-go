package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAppMCPCheck(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "mcp.json")
	if err := os.WriteFile(configPath, []byte(`{
  "servers": [
    {
      "name": "demo",
      "transport": "stdio",
      "command": "cat"
    }
  ]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := NewApp(&stdout, &stderr)
	code := app.Run(context.Background(), []string{"mcp", "check", "--mcp-config", configPath})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if stdout.Len() == 0 {
		t.Fatal("expected output from mcp check")
	}
}
