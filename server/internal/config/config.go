package config

import (
	"encoding/json"
	"os"
	"time"
)

// Config holds all configuration for the application.
type Config struct {
	Server             ServerConfig             `json:"server"`
	Webserver          WebserverConfig          `json:"webserver"`
	Database           DatabaseConfig           `json:"database"`
	AgentStatusService AgentStatusServiceConfig `json:"agent_status_service"`
	Logging            LoggingConfig            `json:"logging"`
	Telegram           TelegramConfig           `json:"telegram"`
}

// ServerConfig holds the server specific configuration.
type ServerConfig struct {
	ListenAddress string `json:"listen_address"`
}

// WebserverConfig holds the webserver specific configuration.
type WebserverConfig struct {
	ListenAddress string `json:"listen_address"`
}

// DatabaseConfig holds the database connection configuration.
type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
	SSLMode  string `json:"sslmode"`
}

// AgentStatusServiceConfig holds the configuration for the agent status service.
type AgentStatusServiceConfig struct {
	CheckIntervalRaw     string `json:"check_interval"`
	InactiveThresholdRaw string `json:"inactive_threshold"`
	CheckInterval        time.Duration
	InactiveThreshold    time.Duration
}

// LoggingConfig holds the logging configuration.
type LoggingConfig struct {
	Directory   string `json:"directory"`
	FilePattern string `json:"file_pattern"`
	MaxAgeHours int    `json:"max_age_hours"`
	LogLevel    string `json:"log_level"`
}

// TelegramConfig holds the configuration for Telegram notifications.
type TelegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   int64  `json:"chat_id"`
}

// Load reads a configuration file from the given path and returns a Config struct.
func Load(path string) (*Config, error) {
	configFile, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(configFile, &cfg); err != nil {
		return nil, err
	}

	// Parse duration strings
	cfg.AgentStatusService.CheckInterval, err = time.ParseDuration(cfg.AgentStatusService.CheckIntervalRaw)
	if err != nil {
		return nil, err
	}
	cfg.AgentStatusService.InactiveThreshold, err = time.ParseDuration(cfg.AgentStatusService.InactiveThresholdRaw)
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}
