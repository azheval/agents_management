package storage

//go:generate mockgen -source=storage.go -destination=mocks/mock_storage.go -package=mocks

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// AgentRepository defines the interface for interacting with agent storage.
type AgentRepository interface {
	CreateAgent(ctx context.Context, agent *Agent) error
	GetAgentByID(ctx context.Context, id uuid.UUID) (*Agent, error)
	UpdateAgentStatus(ctx context.Context, id uuid.UUID, status AgentStatus, lastHeartbeat time.Time) error
	ListAgents(ctx context.Context) ([]*Agent, error)
	ListAgentsByStatus(ctx context.Context, status AgentStatus) ([]*Agent, error)
	DeleteAgent(ctx context.Context, id uuid.UUID) error
	SetOfflineStatusForInactiveAgents(ctx context.Context, inactiveThreshold time.Duration) error
}

// TaskRepository defines the interface for interacting with task storage.
type TaskRepository interface {
	CreateTask(ctx context.Context, task *Task) error
	GetTaskByID(ctx context.Context, taskID uuid.UUID) (*Task, error)
	GetPendingTasksByAgent(ctx context.Context, agentID uuid.UUID) ([]*Task, error)
	ListTasks(ctx context.Context) ([]*Task, error)
	ListTasksByAgentID(ctx context.Context, agentID uuid.UUID) ([]*Task, error)
	UpdateTaskStatus(ctx context.Context, taskID uuid.UUID, status TaskStatus) error
	UpdateTaskStatusIfCurrent(ctx context.Context, taskID uuid.UUID, currentStatus TaskStatus, newStatus TaskStatus) (bool, error)
	CreateTaskResult(ctx context.Context, result *TaskResult) error
	GetTaskResultByTaskID(ctx context.Context, taskID uuid.UUID) (*TaskResult, error)
	GetScheduledTasks(ctx context.Context) ([]Task, error)
	GetTasksByPrerequisite(ctx context.Context, prerequisiteID uuid.UUID) ([]Task, error)
	UpdateTaskSchedule(ctx context.Context, taskID uuid.UUID, scheduleType sql.NullString, scheduledAt sql.NullTime, cronExpression sql.NullString, prerequisiteTaskID uuid.NullUUID) error
}

// LogRepository defines the interface for interacting with log storage.
type LogRepository interface {
	CreateLogEntries(ctx context.Context, logs []*Log) error
	GetLogsByTaskID(ctx context.Context, taskID uuid.UUID) ([]*Log, error)
}

// MetricRepository defines the interface for interacting with agent metrics storage.
type MetricRepository interface {
	StoreAgentMetric(ctx context.Context, metric *AgentMetric) error
	GetMetricsByAgentID(ctx context.Context, agentID uuid.UUID, since time.Time) ([]*AgentMetric, error)
}

// Storage is a container for all repositories.
type Storage struct {
	Agent  AgentRepository
	Task   TaskRepository
	Log    LogRepository
	Metric MetricRepository
}

// NewStorage creates a new storage container.
func NewStorage(agentRepo AgentRepository, taskRepo TaskRepository, logRepo LogRepository, metricRepo MetricRepository) *Storage {
	return &Storage{
		Agent:  agentRepo,
		Task:   taskRepo,
		Log:    logRepo,
		Metric: metricRepo,
	}
}
