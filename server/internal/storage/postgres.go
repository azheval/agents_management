package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
	"github.com/jmoiron/sqlx"
)

// PostgresAgentRepository is the PostgreSQL implementation of the AgentRepository.
type PostgresAgentRepository struct {
	db *sqlx.DB
}

// NewPostgresAgentRepository creates a new repository for agents.
func NewPostgresAgentRepository(db *sqlx.DB) *PostgresAgentRepository {
	return &PostgresAgentRepository{db: db}
}

func (r *PostgresAgentRepository) CreateAgent(ctx context.Context, agent *Agent) error {
	query := `
		INSERT INTO agents (id, hostname, os, arch, agent_version, ip_addresses, last_heartbeat, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO UPDATE SET
			hostname = EXCLUDED.hostname,
			os = EXCLUDED.os,
			arch = EXCLUDED.arch,
			agent_version = EXCLUDED.agent_version,
			ip_addresses = EXCLUDED.ip_addresses,
			last_heartbeat = EXCLUDED.last_heartbeat,
			status = EXCLUDED.status,
			updated_at = NOW();
	`
	_, err := r.db.ExecContext(ctx, query, agent.ID, agent.Hostname, agent.OS, agent.Arch, agent.AgentVersion, agent.IPAddresses, agent.LastHeartbeat, agent.Status)
	return err
}

func (r *PostgresAgentRepository) GetAgentByID(ctx context.Context, id uuid.UUID) (*Agent, error) {
	var agent Agent
	query := "SELECT * FROM agents WHERE id = $1"
	err := r.db.GetContext(ctx, &agent, query, id)
	return &agent, err
}

func (r *PostgresAgentRepository) UpdateAgentStatus(ctx context.Context, id uuid.UUID, status AgentStatus, lastHeartbeat time.Time) error {
	query := "UPDATE agents SET status = $1, last_heartbeat = $2, updated_at = NOW() WHERE id = $3"
	_, err := r.db.ExecContext(ctx, query, status, lastHeartbeat, id)
	return err
}

func (r *PostgresAgentRepository) ListAgents(ctx context.Context) ([]*Agent, error) {
	var agents []*Agent
	query := "SELECT * FROM agents ORDER BY created_at DESC"
	err := r.db.SelectContext(ctx, &agents, query)
	return agents, err
}

func (r *PostgresAgentRepository) ListAgentsByStatus(ctx context.Context, status AgentStatus) ([]*Agent, error) {
	var agents []*Agent
	query := "SELECT * FROM agents WHERE status = $1 ORDER BY created_at DESC"
	err := r.db.SelectContext(ctx, &agents, query, status)
	return agents, err
}

func (r *PostgresAgentRepository) DeleteAgent(ctx context.Context, id uuid.UUID) error {
	query := "DELETE FROM agents WHERE id = $1"
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

func (r *PostgresAgentRepository) SetOfflineStatusForInactiveAgents(ctx context.Context, inactiveThreshold time.Duration) error {
	query := `
		UPDATE agents
		SET status = 'offline'
		WHERE status = 'online' AND last_heartbeat < NOW() - interval '1 second' * $1;
	`
	_, err := r.db.ExecContext(ctx, query, int64(inactiveThreshold.Seconds()))
	return err
}

// PostgresTaskRepository is the PostgreSQL implementation of the TaskRepository.
type PostgresTaskRepository struct {
	db *sqlx.DB
}

// NewPostgresTaskRepository creates a new repository for tasks.
func NewPostgresTaskRepository(db *sqlx.DB) *PostgresTaskRepository {
	return &PostgresTaskRepository{db: db}
}

func (r *PostgresTaskRepository) CreateTask(ctx context.Context, task *Task) error {
	query := `
		INSERT INTO tasks (
			id, agent_id, description, task_type, command, args, entrypoint_script, package_files, 
			source_path, destination_path, timeout_seconds, status, 
			schedule_type, cron_expression, prerequisite_task_id, scheduled_at, created_by
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);
	`
	_, err := r.db.ExecContext(ctx, query,
		task.ID, task.AgentID, task.Description, task.TaskType, task.Command, task.Args, task.EntrypointScript,
		task.PackageFiles, task.SourcePath, task.DestinationPath, task.TimeoutSeconds, task.Status,
		task.ScheduleType, task.CronExpression, task.PrerequisiteTaskID, task.ScheduledAt, task.CreatedBy,
	)
	return err
}

func (r *PostgresTaskRepository) GetPendingTasksByAgent(ctx context.Context, agentID uuid.UUID) ([]*Task, error) {
	var tasks []*Task
	query := `
		SELECT * FROM tasks
		WHERE agent_id = $1 AND status = 'pending'
		ORDER BY created_at ASC;
	`
	err := r.db.SelectContext(ctx, &tasks, query, agentID)
	return tasks, err
}

func (r *PostgresTaskRepository) ListTasks(ctx context.Context) ([]*Task, error) {
	var tasks []*Task
	query := "SELECT * FROM tasks ORDER BY created_at DESC"
	err := r.db.SelectContext(ctx, &tasks, query)
	return tasks, err
}

func (r *PostgresTaskRepository) ListTasksByAgentID(ctx context.Context, agentID uuid.UUID) ([]*Task, error) {
	var tasks []*Task
	query := "SELECT * FROM tasks WHERE agent_id = $1 ORDER BY created_at DESC"
	err := r.db.SelectContext(ctx, &tasks, query, agentID)
	return tasks, err
}

func (r *PostgresTaskRepository) GetTaskByID(ctx context.Context, taskID uuid.UUID) (*Task, error) {
	var task Task
	query := "SELECT * FROM tasks WHERE id = $1"
	err := r.db.GetContext(ctx, &task, query, taskID)
	return &task, err
}

func (r *PostgresTaskRepository) UpdateTaskStatus(ctx context.Context, taskID uuid.UUID, status TaskStatus) error {
	query := "UPDATE tasks SET status = $1, updated_at = NOW() WHERE id = $2"
	_, err := r.db.ExecContext(ctx, query, status, taskID)
	return err
}

func (r *PostgresTaskRepository) UpdateTaskStatusIfCurrent(ctx context.Context, taskID uuid.UUID, currentStatus TaskStatus, newStatus TaskStatus) (bool, error) {
	query := "UPDATE tasks SET status = $1, updated_at = NOW() WHERE id = $2 AND status = $3"
	result, err := r.db.ExecContext(ctx, query, newStatus, taskID, currentStatus)
	if err != nil {
		return false, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return rowsAffected > 0, nil
}

func (r *PostgresTaskRepository) CreateTaskResult(ctx context.Context, result *TaskResult) error {
	query := `
		INSERT INTO task_results (id, task_id, agent_id, status, exit_code, output, output_file_path, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8);
	`
	_, err := r.db.ExecContext(ctx, query, result.ID, result.TaskID, result.AgentID, result.Status, result.ExitCode, result.Output, result.OutputFilePath, result.DurationMs)
	return err
}

func (r *PostgresTaskRepository) GetTaskResultByTaskID(ctx context.Context, taskID uuid.UUID) (*TaskResult, error) {
	var result TaskResult
	query := "SELECT * FROM task_results WHERE task_id = $1"
	err := r.db.GetContext(ctx, &result, query, taskID)
	return &result, err
}

func (r *PostgresTaskRepository) GetScheduledTasks(ctx context.Context) ([]Task, error) {
	var tasks []Task
	query := "SELECT * FROM tasks WHERE schedule_type IS NOT NULL AND status = 'pending'"
	err := r.db.SelectContext(ctx, &tasks, query)
	return tasks, err
}

func (r *PostgresTaskRepository) GetTasksByPrerequisite(ctx context.Context, prerequisiteID uuid.UUID) ([]Task, error) {
	var tasks []Task
	query := "SELECT * FROM tasks WHERE prerequisite_task_id = $1 AND status = 'pending'"
	err := r.db.SelectContext(ctx, &tasks, query, prerequisiteID)
	return tasks, err
}

func (r *PostgresTaskRepository) UpdateTaskSchedule(ctx context.Context, taskID uuid.UUID, scheduleType sql.NullString, scheduledAt sql.NullTime, cronExpression sql.NullString, prerequisiteTaskID uuid.NullUUID) error {
	query := `
		UPDATE tasks SET
			schedule_type = $1,
			scheduled_at = $2,
			cron_expression = $3,
			prerequisite_task_id = $4,
			updated_at = NOW()
		WHERE id = $5
	`
	_, err := r.db.ExecContext(ctx, query, scheduleType, scheduledAt, cronExpression, prerequisiteTaskID, taskID)
	return err
}

// PostgresLogRepository is the PostgreSQL implementation of the LogRepository.
type PostgresLogRepository struct {
	db *sqlx.DB
}

// NewPostgresLogRepository creates a new repository for logs.
func NewPostgresLogRepository(db *sqlx.DB) *PostgresLogRepository {
	return &PostgresLogRepository{db: db}
}

// CreateLogEntries inserts multiple log entries in a single transaction.
func (r *PostgresLogRepository) CreateLogEntries(ctx context.Context, logs []*Log) error {
	if len(logs) == 0 {
		return nil
	}

	tx, err := r.db.Beginx() // Use Beginx for sqlx transaction
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback is a no-op if the transaction is committed.

	stmt, err := tx.PreparexContext(ctx, "INSERT INTO logs (id, agent_id, task_id, timestamp, level, message) VALUES ($1, $2, $3, $4, $5, $6)")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, log := range logs {
		if _, err := stmt.ExecContext(ctx, log.ID, log.AgentID, log.TaskID, log.Timestamp, log.Level, log.Message); err != nil {
			return fmt.Errorf("failed to execute statement for log entry: %w", err)
		}
	}

	return tx.Commit()
}

func (r *PostgresLogRepository) GetLogsByTaskID(ctx context.Context, taskID uuid.UUID) ([]*Log, error) {
	var logs []*Log
	query := "SELECT * FROM logs WHERE task_id = $1 ORDER BY timestamp ASC"
	err := r.db.SelectContext(ctx, &logs, query, taskID)
	return logs, err
}

// DBConfig holds the configuration for the database connection.
type DBConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	DBName   string
	SSLMode  string
}

// NewDBConnection creates a new database connection pool.
func NewDBConnection(cfg DBConfig) (*sqlx.DB, error) {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.DBName, cfg.SSLMode)

	db, err := sqlx.Connect("pgx", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	if err = db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}

// PostgresMetricRepository is the PostgreSQL implementation of the MetricRepository.
type PostgresMetricRepository struct {
	db *sqlx.DB
}

// NewPostgresMetricRepository creates a new repository for metrics.
func NewPostgresMetricRepository(db *sqlx.DB) *PostgresMetricRepository {
	return &PostgresMetricRepository{db: db}
}

// StoreAgentMetric inserts a new agent metric record into the database.
func (r *PostgresMetricRepository) StoreAgentMetric(ctx context.Context, metric *AgentMetric) error {
	query := `
		INSERT INTO agent_metrics (agent_id, timestamp, cpu_usage, ram_usage, disk_usage)
		VALUES ($1, $2, $3, $4, $5);
	`
	_, err := r.db.ExecContext(ctx, query, metric.AgentID, metric.Timestamp, metric.CPUUsage, metric.RAMUsage, metric.DiskUsage)
	return err
}

// GetMetricsByAgentID retrieves all metrics for a given agent since a certain time.
func (r *PostgresMetricRepository) GetMetricsByAgentID(ctx context.Context, agentID uuid.UUID, since time.Time) ([]*AgentMetric, error) {
	var metrics []*AgentMetric
	query := `
		SELECT agent_id, timestamp, cpu_usage, ram_usage, disk_usage, created_at
		FROM agent_metrics
		WHERE agent_id = $1 AND timestamp >= $2
		ORDER BY timestamp ASC;
	`
	err := r.db.SelectContext(ctx, &metrics, query, agentID, since)
	return metrics, err
}
