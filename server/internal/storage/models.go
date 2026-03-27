package storage

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type AgentStatus string

const (
	AgentStatusOnline       AgentStatus = "online"
	AgentStatusOffline      AgentStatus = "offline"
	AgentStatusDisconnected AgentStatus = "disconnected"
)

type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusAssigned  TaskStatus = "assigned"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusTimedOut  TaskStatus = "timed_out"
)

type TaskType string

const (
	TaskTypeExecCommand      TaskType = "EXEC_COMMAND"
	TaskTypeExecPythonScript TaskType = "EXEC_PYTHON_SCRIPT"
	TaskTypeFetchFile        TaskType = "FETCH_FILE"
	TaskTypePushFile         TaskType = "PUSH_FILE"
	TaskTypeAgentUpdate      TaskType = "AGENT_UPDATE"
)

// Agent corresponds to the 'agents' table in the database.
type Agent struct {
	ID            uuid.UUID      `db:"id"`
	Hostname      string         `db:"hostname"`
	OS            sql.NullString `db:"os"`
	Arch          sql.NullString `db:"arch"`
	AgentVersion  sql.NullString `db:"agent_version"`
	IPAddresses   pq.StringArray `db:"ip_addresses"`
	LastHeartbeat sql.NullTime   `db:"last_heartbeat"`
	Status        AgentStatus    `db:"status"`
	CreatedAt     time.Time      `db:"created_at"`
	UpdatedAt     time.Time      `db:"updated_at"`
}

// Task corresponds to the 'tasks' table in the database.
type Task struct {
	ID                 uuid.UUID      `db:"id" json:"id"`
	AgentID            uuid.UUID      `db:"agent_id" json:"agentId"`
	Description        sql.NullString `db:"description" json:"description"`
	TaskType           TaskType       `db:"task_type" json:"taskType"`
	Command            sql.NullString `db:"command" json:"command"`
	Args               pq.StringArray `db:"args" json:"args"`
	EntrypointScript   sql.NullString `db:"entrypoint_script" json:"entrypointScript"`
	PackageFiles       pq.StringArray `db:"package_files" json:"packageFiles"`
	SourcePath         sql.NullString `db:"source_path" json:"sourcePath"`
	DestinationPath    sql.NullString `db:"destination_path" json:"destinationPath"`
	TimeoutSeconds     sql.NullInt32  `db:"timeout_seconds" json:"timeoutSeconds"`
	Status             TaskStatus     `db:"status" json:"status"`
	ScheduleType       sql.NullString `db:"schedule_type" json:"scheduleType"`
	CronExpression     sql.NullString `db:"cron_expression" json:"cronExpression"`
	PrerequisiteTaskID uuid.NullUUID  `db:"prerequisite_task_id" json:"prerequisiteTaskId"`
	ScheduledAt        sql.NullTime   `db:"scheduled_at" json:"scheduledAt"`
	StartedAt          sql.NullTime   `db:"started_at" json:"startedAt"`
	CompletedAt        sql.NullTime   `db:"completed_at" json:"completedAt"`
	CreatedBy          sql.NullString `db:"created_by" json:"createdBy"`
	CreatedAt          time.Time      `db:"created_at" json:"createdAt"`
	UpdatedAt          time.Time      `db:"updated_at" json:"updatedAt"`
}

type TaskResultStatus string

const (
	TaskResultStatusSuccess  TaskResultStatus = "SUCCESS"
	TaskResultStatusFailed   TaskResultStatus = "FAILED"
	TaskResultStatusTimedOut TaskResultStatus = "TIMED_OUT"
)

type LogLevel string

const (
	LogLevelInfo  LogLevel = "INFO"
	LogLevelWarn  LogLevel = "WARN"
	LogLevelError LogLevel = "ERROR"
	LogLevelDebug LogLevel = "DEBUG"
)

// TaskResult corresponds to the 'task_results' table in the database.
type TaskResult struct {
	ID             uuid.UUID        `db:"id"`
	TaskID         uuid.UUID        `db:"task_id"`
	AgentID        uuid.UUID        `db:"agent_id"`
	Status         TaskResultStatus `db:"status"`
	ExitCode       sql.NullInt32    `db:"exit_code"`
	Output         sql.NullString   `db:"output"`
	OutputFilePath sql.NullString   `db:"output_file_path"`
	DurationMs     sql.NullInt64    `db:"duration_ms"`
	RecordedAt     time.Time        `db:"recorded_at"`
}

// Log corresponds to the 'logs' table in the database.
type Log struct {
	ID        uuid.UUID `db:"id"`
	AgentID   uuid.UUID `db:"agent_id"`
	TaskID    uuid.UUID `db:"task_id"`
	Timestamp time.Time `db:"timestamp"`
	Level     LogLevel  `db:"level"`
	Message   string    `db:"message"`
}

// AgentMetric corresponds to the 'agent_metrics' table.
type AgentMetric struct {
	ID        uuid.UUID `db:"id"`
	AgentID   uuid.UUID `db:"agent_id"`
	Timestamp time.Time `db:"timestamp"`
	CPUUsage  float32   `db:"cpu_usage"`
	RAMUsage  float32   `db:"ram_usage"`
	DiskUsage []byte    `db:"disk_usage"` // JSONB stored as raw bytes
	CreatedAt time.Time `db:"created_at"`
}
