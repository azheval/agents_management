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
	MarkTaskStartedIfCurrent(ctx context.Context, taskID uuid.UUID, currentStatus TaskStatus, startedAt time.Time) (bool, error)
	MarkTaskCompleted(ctx context.Context, taskID uuid.UUID, status TaskStatus, completedAt time.Time) error
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

// NotificationRepository defines the interface for interacting with notification storage.
type NotificationRepository interface {
	CreateNotificationEvent(ctx context.Context, event *NotificationEvent) error
	UpdateNotificationEventStatus(ctx context.Context, id uuid.UUID, status NotificationEventStatus) error
	GetNotificationEventByID(ctx context.Context, id uuid.UUID) (*NotificationEvent, error)
	FindLatestNotificationEventByDedupKeySince(ctx context.Context, dedupKey string, since time.Time, excludeID uuid.UUID) (*NotificationEvent, error)
	ListNotificationEvents(ctx context.Context, limit int) ([]*NotificationEvent, error)
	ListNotificationEventsByTaskID(ctx context.Context, taskID uuid.UUID) ([]*NotificationEvent, error)
	CreateNotificationDelivery(ctx context.Context, delivery *NotificationDelivery) error
	UpdateNotificationDeliveryStatus(ctx context.Context, id uuid.UUID, attempt int32, status NotificationDeliveryStatus, providerMessageID sql.NullString, providerResponseJSON []byte, errorMessage sql.NullString, lastErrorCode sql.NullString, sentAt sql.NullTime, nextRetryAt sql.NullTime) error
	GetNotificationDeliveryByID(ctx context.Context, id uuid.UUID) (*NotificationDelivery, error)
	ListNotificationDeliveriesForDispatch(ctx context.Context, now time.Time, limit int) ([]*NotificationDelivery, error)
	ListNotificationDeliveriesByEventID(ctx context.Context, eventID uuid.UUID) ([]*NotificationDelivery, error)
	ScheduleNotificationDeliveryRetry(ctx context.Context, id uuid.UUID, maxAttempts int32, nextRetryAt time.Time) error
}

// UserRepository defines access to server operator accounts and their roles.
type UserRepository interface {
	AuthenticateUser(ctx context.Context, username, password string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	ListUsers(ctx context.Context) ([]*User, error)
	ListRoles(ctx context.Context) ([]*Role, error)
	CreateUser(ctx context.Context, username, password string) error
	UpdateUserPassword(ctx context.Context, username, password string) error
	UpdateUserStatus(ctx context.Context, username string, isActive bool) error
	SetUserRoles(ctx context.Context, username string, roleNames []string) error
	EnsureRoles(ctx context.Context, roles []*Role) error
}

// ExecPolicyRepository defines access to saved EXEC_COMMAND policies and their agent bindings.
type ExecPolicyRepository interface {
	CreateExecCommandPolicy(ctx context.Context, policy *ExecCommandPolicy) error
	UpdateExecCommandPolicy(ctx context.Context, policy *ExecCommandPolicy) error
	DeleteExecCommandPolicy(ctx context.Context, id uuid.UUID) error
	ListExecCommandPolicies(ctx context.Context) ([]*ExecCommandPolicy, error)
	GetExecCommandPolicyByID(ctx context.Context, id uuid.UUID) (*ExecCommandPolicy, error)
	CreateExecCommandPolicyBinding(ctx context.Context, binding *ExecCommandPolicyBinding) error
	UpdateExecCommandPolicyBinding(ctx context.Context, binding *ExecCommandPolicyBinding) error
	DeleteExecCommandPolicyBinding(ctx context.Context, id uuid.UUID) error
	ListExecCommandPolicyBindings(ctx context.Context) ([]*ExecCommandPolicyBinding, error)
	GetExecCommandPolicyBinding(ctx context.Context, policyID, agentID uuid.UUID) (*ExecCommandPolicyBinding, error)
}

// Storage is a container for all repositories.
type Storage struct {
	Agent        AgentRepository
	Task         TaskRepository
	Log          LogRepository
	Metric       MetricRepository
	Notification NotificationRepository
	User         UserRepository
	ExecPolicy   ExecPolicyRepository
}

// NewStorage creates a new storage container.
func NewStorage(agentRepo AgentRepository, taskRepo TaskRepository, logRepo LogRepository, metricRepo MetricRepository, notificationRepo NotificationRepository, userRepo UserRepository, execPolicyRepo ExecPolicyRepository) *Storage {
	return &Storage{
		Agent:        agentRepo,
		Task:         taskRepo,
		Log:          logRepo,
		Metric:       metricRepo,
		Notification: notificationRepo,
		User:         userRepo,
		ExecPolicy:   execPolicyRepo,
	}
}
