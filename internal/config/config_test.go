package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleYAML = "server:\n  port: ${PORT:8080}\nenvironment: ${ENVIRONMENT:local}\n"

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}
	return path
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantPort    string
		wantEnviron string
	}{
		{
			name:        "defaults when env unset",
			env:         nil,
			wantPort:    "8080",
			wantEnviron: "local",
		},
		{
			name:        "env values win",
			env:         map[string]string{"PORT": "9090", "ENVIRONMENT": "staging"},
			wantPort:    "9090",
			wantEnviron: "staging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			path := writeConfig(t, sampleYAML)

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if cfg.Server.Port != tt.wantPort {
				t.Errorf("port = %q, want %q", cfg.Server.Port, tt.wantPort)
			}
			if cfg.Environment != tt.wantEnviron {
				t.Errorf("environment = %q, want %q", cfg.Environment, tt.wantEnviron)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yml"))
	if err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
}
