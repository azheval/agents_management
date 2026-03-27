package notifier

import (
	"agent-management/server/internal/storage"
	"log/slog"
)

// Notifier defines the interface for sending notifications.
type Notifier interface {
	NotifyTaskCompletion(task *storage.Task, result *storage.TaskResult) error
	NotifyAgentStatusChange(agent *storage.Agent, oldStatus, newStatus storage.AgentStatus) error
}

// MultiNotifier holds multiple notifiers and sends notifications to all of them.
type MultiNotifier struct {
	notifiers []Notifier
	logger    *slog.Logger
}

// NewMultiNotifier creates a new MultiNotifier.
func NewMultiNotifier(logger *slog.Logger, notifiers ...Notifier) *MultiNotifier {
	return &MultiNotifier{
		notifiers: notifiers,
		logger:    logger,
	}
}

// NotifyTaskCompletion sends task completion notifications to all configured notifiers.
func (m *MultiNotifier) NotifyTaskCompletion(task *storage.Task, result *storage.TaskResult) error {
	for _, n := range m.notifiers {
		if err := n.NotifyTaskCompletion(task, result); err != nil {
			m.logger.Warn("Failed to send notification", "notifier", n, "error", err)
			// Continue to other notifiers
		}
	}
	return nil
}

// NotifyAgentStatusChange sends agent status change notifications to all configured notifiers.
func (m *MultiNotifier) NotifyAgentStatusChange(agent *storage.Agent, oldStatus, newStatus storage.AgentStatus) error {
	for _, n := range m.notifiers {
		if err := n.NotifyAgentStatusChange(agent, oldStatus, newStatus); err != nil {
			m.logger.Warn("Failed to send agent status notification", "notifier", n, "error", err)
			// Continue to other notifiers
		}
	}
	return nil
}

// LogNotifier is a simple notifier that just logs messages. Useful for debugging.
type LogNotifier struct {
	logger *slog.Logger
}

// NewLogNotifier creates a new LogNotifier.
func NewLogNotifier(logger *slog.Logger) *LogNotifier {
	return &LogNotifier{logger: logger.With("component", "LogNotifier")}
}

// NotifyTaskCompletion logs the task completion event.
func (n *LogNotifier) NotifyTaskCompletion(task *storage.Task, result *storage.TaskResult) error {
	n.logger.Info("Task completed notification",
		"task_id", task.ID,
		"task_type", task.TaskType,
		"agent_id", task.AgentID,
		"final_status", result.Status,
	)
	return nil
}

// NotifyAgentStatusChange logs the agent status change event.
func (n *LogNotifier) NotifyAgentStatusChange(agent *storage.Agent, oldStatus, newStatus storage.AgentStatus) error {
	n.logger.Info("Agent status changed notification",
		"agent_id", agent.ID,
		"hostname", agent.Hostname,
		"old_status", oldStatus,
		"new_status", newStatus,
	)
	return nil
}
