package internal

import (
	"context"
	"log/slog"
	"time"

	"agent-management/server/internal/storage"
)

// AgentStatusService periodically checks for inactive agents and updates their status to offline.
type AgentStatusService struct {
	logger            *slog.Logger
	storage           *storage.Storage
	ticker            *time.Ticker
	checkInterval     time.Duration
	inactiveThreshold time.Duration
}

// NewAgentStatusService creates a new service for monitoring agent statuses.
func NewAgentStatusService(logger *slog.Logger, storage *storage.Storage, checkInterval, inactiveThreshold time.Duration) *AgentStatusService {
	return &AgentStatusService{
		logger:            logger,
		storage:           storage,
		ticker:            time.NewTicker(checkInterval),
		checkInterval:     checkInterval,
		inactiveThreshold: inactiveThreshold,
	}
}

// Start begins the periodic checking for inactive agents.
// It runs in a loop and should be started as a goroutine.
func (s *AgentStatusService) Start(ctx context.Context) {
	s.logger.Info("Starting agent status service", "check_interval", s.checkInterval.String(), "inactive_threshold", s.inactiveThreshold.String())
	defer s.ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Stopping agent status service")
			return
		case <-s.ticker.C:
			s.logger.Debug("Running check for inactive agents")
			if err := s.storage.Agent.SetOfflineStatusForInactiveAgents(ctx, s.inactiveThreshold); err != nil {
				s.logger.Error("Failed to update status for inactive agents", "error", err)
			}
		}
	}
}
