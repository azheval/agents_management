package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	// Create a temporary config file for testing
	content := `
{
  "server": {
    "listen_address": ":8080"
  },
  "database": {
    "host": "testhost",
    "port": "5433",
    "username": "testuser",
    "password": "testpassword",
    "dbname": "testdb",
    "sslmode": "require"
  },
  "agent_status_service": {
    "check_interval": "30s",
    "inactive_threshold": "90s"
  },
  "logging": {
    "directory": "/tmp/logs",
    "file_pattern": "test.log",
    "max_age_hours": 24,
    "log_level": "DEBUG"
  }
}
`
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to create temp config file: %v", err)
	}

	// Load the config
	cfg, err := Load(configFile)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Assert values
	if cfg.Server.ListenAddress != ":8080" {
		t.Errorf("expected listen_address %q, got %q", ":8080", cfg.Server.ListenAddress)
	}
	if cfg.Database.Host != "testhost" {
		t.Errorf("expected db host %q, got %q", "testhost", cfg.Database.Host)
	}
	if cfg.AgentStatusService.CheckInterval != 30*time.Second {
		t.Errorf("expected check_interval %v, got %v", 30*time.Second, cfg.AgentStatusService.CheckInterval)
	}
	if cfg.AgentStatusService.InactiveThreshold != 90*time.Second {
		t.Errorf("expected inactive_threshold %v, got %v", 90*time.Second, cfg.AgentStatusService.InactiveThreshold)
	}
	if cfg.Logging.LogLevel != "DEBUG" {
		t.Errorf("expected log_level %q, got %q", "DEBUG", cfg.Logging.LogLevel)
	}
}

func TestLoad_FileNotExists(t *testing.T) {
	_, err := Load("non_existent_file.json")
	if err == nil {
		t.Fatal("Load() should have failed for a non-existent file, but it did not")
	}
}
