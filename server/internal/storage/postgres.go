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
			source_path, destination_path, result_contract, notification_rule_set, default_destinations, timeout_seconds, status, 
			schedule_type, cron_expression, prerequisite_task_id, scheduled_at, created_by
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20);
	`
	_, err := r.db.ExecContext(ctx, query,
		task.ID, task.AgentID, task.Description, task.TaskType, task.Command, task.Args, task.EntrypointScript,
		task.PackageFiles, task.SourcePath, task.DestinationPath, task.ResultContract, task.NotificationRuleSet, task.DefaultDestinations, task.TimeoutSeconds, task.Status,
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

// PostgresNotificationRepository is the PostgreSQL implementation of the NotificationRepository.
type PostgresNotificationRepository struct {
	db *sqlx.DB
}

// NewPostgresNotificationRepository creates a new repository for notifications.
func NewPostgresNotificationRepository(db *sqlx.DB) *PostgresNotificationRepository {
	return &PostgresNotificationRepository{db: db}
}

func (r *PostgresNotificationRepository) CreateNotificationEvent(ctx context.Context, event *NotificationEvent) error {
	query := `
		INSERT INTO notification_events (
			id, task_id, agent_id, prerequisite_task_id, event_type, severity, title, summary,
			source_kind, source_path, source_ref, payload_json, dedup_key, dedup_window_seconds, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);
	`
	_, err := r.db.ExecContext(ctx, query,
		event.ID, event.TaskID, event.AgentID, event.PrerequisiteTaskID, event.EventType, event.Severity,
		event.Title, event.Summary, event.SourceKind, event.SourcePath, event.SourceRef, event.PayloadJSON,
		event.DedupKey, event.DedupWindowSeconds, event.Status,
	)
	return err
}

func (r *PostgresNotificationRepository) UpdateNotificationEventStatus(ctx context.Context, id uuid.UUID, status NotificationEventStatus) error {
	query := `
		UPDATE notification_events
		SET status = $1
		WHERE id = $2
	`
	_, err := r.db.ExecContext(ctx, query, status, id)
	return err
}

func (r *PostgresNotificationRepository) GetNotificationEventByID(ctx context.Context, id uuid.UUID) (*NotificationEvent, error) {
	var event NotificationEvent
	query := "SELECT * FROM notification_events WHERE id = $1"
	err := r.db.GetContext(ctx, &event, query, id)
	return &event, err
}

func (r *PostgresNotificationRepository) FindLatestNotificationEventByDedupKeySince(ctx context.Context, dedupKey string, since time.Time, excludeID uuid.UUID) (*NotificationEvent, error) {
	var event NotificationEvent
	query := `
		SELECT *
		FROM notification_events
		WHERE dedup_key = $1
		  AND created_at >= $2
		  AND id <> $3
		ORDER BY created_at DESC
		LIMIT 1
	`
	err := r.db.GetContext(ctx, &event, query, dedupKey, since, excludeID)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func (r *PostgresNotificationRepository) ListNotificationEvents(ctx context.Context, limit int) ([]*NotificationEvent, error) {
	var events []*NotificationEvent
	query := "SELECT * FROM notification_events ORDER BY created_at DESC LIMIT $1"
	err := r.db.SelectContext(ctx, &events, query, limit)
	return events, err
}

func (r *PostgresNotificationRepository) ListNotificationEventsByTaskID(ctx context.Context, taskID uuid.UUID) ([]*NotificationEvent, error) {
	var events []*NotificationEvent
	query := "SELECT * FROM notification_events WHERE task_id = $1 ORDER BY created_at DESC"
	err := r.db.SelectContext(ctx, &events, query, taskID)
	return events, err
}

func (r *PostgresNotificationRepository) CreateNotificationDelivery(ctx context.Context, delivery *NotificationDelivery) error {
	query := `
		INSERT INTO notification_deliveries (
			id, notification_event_id, channel, destination, status, attempt, max_attempts,
			provider_message_id, provider_response_json, error_message, last_error_code,
			scheduled_at, sent_at, next_retry_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);
	`
	_, err := r.db.ExecContext(ctx, query,
		delivery.ID, delivery.NotificationEventID, delivery.Channel, delivery.Destination, delivery.Status,
		delivery.Attempt, delivery.MaxAttempts, delivery.ProviderMessageID, delivery.ProviderResponseJSON,
		delivery.ErrorMessage, delivery.LastErrorCode, delivery.ScheduledAt, delivery.SentAt, delivery.NextRetryAt,
	)
	return err
}

func (r *PostgresNotificationRepository) UpdateNotificationDeliveryStatus(ctx context.Context, id uuid.UUID, attempt int32, status NotificationDeliveryStatus, providerMessageID sql.NullString, providerResponseJSON []byte, errorMessage sql.NullString, lastErrorCode sql.NullString, sentAt sql.NullTime, nextRetryAt sql.NullTime) error {
	query := `
		UPDATE notification_deliveries
		SET attempt = $1,
			status = $2,
			provider_message_id = $3,
			provider_response_json = $4,
			error_message = $5,
			last_error_code = $6,
			sent_at = $7,
			next_retry_at = $8
		WHERE id = $9
	`
	_, err := r.db.ExecContext(ctx, query, attempt, status, providerMessageID, providerResponseJSON, errorMessage, lastErrorCode, sentAt, nextRetryAt, id)
	return err
}

func (r *PostgresNotificationRepository) GetNotificationDeliveryByID(ctx context.Context, id uuid.UUID) (*NotificationDelivery, error) {
	var delivery NotificationDelivery
	query := "SELECT * FROM notification_deliveries WHERE id = $1"
	err := r.db.GetContext(ctx, &delivery, query, id)
	return &delivery, err
}

func (r *PostgresNotificationRepository) ListNotificationDeliveriesByEventID(ctx context.Context, eventID uuid.UUID) ([]*NotificationDelivery, error) {
	var deliveries []*NotificationDelivery
	query := "SELECT * FROM notification_deliveries WHERE notification_event_id = $1 ORDER BY created_at ASC"
	err := r.db.SelectContext(ctx, &deliveries, query, eventID)
	return deliveries, err
}

func (r *PostgresNotificationRepository) ListNotificationDeliveriesForDispatch(ctx context.Context, now time.Time, limit int) ([]*NotificationDelivery, error) {
	var deliveries []*NotificationDelivery
	query := `
		SELECT *
		FROM notification_deliveries
		WHERE
			(status = 'pending' AND scheduled_at <= $1)
			OR
			(status = 'retry_scheduled' AND next_retry_at IS NOT NULL AND next_retry_at <= $1)
		ORDER BY created_at ASC
		LIMIT $2
	`
	err := r.db.SelectContext(ctx, &deliveries, query, now, limit)
	return deliveries, err
}

func (r *PostgresNotificationRepository) ScheduleNotificationDeliveryRetry(ctx context.Context, id uuid.UUID, maxAttempts int32, nextRetryAt time.Time) error {
	query := `
		UPDATE notification_deliveries
		SET status = 'retry_scheduled',
			max_attempts = $1,
			next_retry_at = $2,
			error_message = NULL,
			last_error_code = NULL,
			provider_response_json = NULL,
			sent_at = NULL
		WHERE id = $3
	`
	_, err := r.db.ExecContext(ctx, query, maxAttempts, nextRetryAt, id)
	return err
}
