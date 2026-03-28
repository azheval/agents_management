package storage

import (
	"context"
	"database/sql"
	"log"
	"os"
	"testing"
	"time"

	"agent-management/server/internal/config"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// StorageTestSuite is the main suite for storage integration tests
type StorageTestSuite struct {
	suite.Suite
	db *sqlx.DB
}

// SetupSuite runs once before the entire test suite
func (s *StorageTestSuite) SetupSuite() {
	cfg, err := config.Load("../../config.json")
	if err != nil {
		log.Fatalf("failed to load config for tests: %v", err)
	}

	db, err := NewDBConnection(DBConfig{
		Host:     cfg.Database.Host,
		Port:     cfg.Database.Port,
		Username: cfg.Database.Username,
		Password: cfg.Database.Password,
		DBName:   cfg.Database.DBName,
		SSLMode:  cfg.Database.SSLMode,
	})
	if err != nil {
		log.Fatalf("failed to connect to db for tests: %v", err)
	}
	s.db = db

	if _, err := s.db.Exec("ALTER TABLE tasks ADD COLUMN IF NOT EXISTS description TEXT"); err != nil {
		log.Fatalf("failed to prepare test schema: %v", err)
	}

	migration009, err := os.ReadFile("../../../db/migrations/009_add_notification_storage.sql")
	if err != nil {
		log.Fatalf("failed to read notification migration: %v", err)
	}
	if _, err := s.db.Exec(string(migration009)); err != nil {
		log.Fatalf("failed to apply notification migration: %v", err)
	}

	migration010, err := os.ReadFile("../../../db/migrations/010_add_task_notification_settings.sql")
	if err != nil {
		log.Fatalf("failed to read task notification settings migration: %v", err)
	}
	if _, err := s.db.Exec(string(migration010)); err != nil {
		log.Fatalf("failed to apply task notification settings migration: %v", err)
	}
}

// TearDownSuite runs once after the entire test suite
func (s *StorageTestSuite) TearDownSuite() {
	s.db.Close()
}

// TestStorageTestSuite runs the entire suite
func TestStorageTestSuite(t *testing.T) {
	suite.Run(t, new(StorageTestSuite))
}

func (s *StorageTestSuite) TestTaskRepository() {
	t := s.T()
	taskRepo := NewPostgresTaskRepository(s.db)
	agentRepo := NewPostgresAgentRepository(s.db)

	// 1. Create agent for the test
	agent := &Agent{
		ID:       uuid.New(),
		Hostname: "task-test-agent",
		Status:   AgentStatusOnline,
	}
	err := agentRepo.CreateAgent(context.Background(), agent)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM agents WHERE id = $1", agent.ID)

	// 2. Create task
	taskID := uuid.New()
	task := &Task{
		ID:                  taskID,
		AgentID:             agent.ID,
		TaskType:            TaskTypeExecCommand,
		Status:              TaskStatusPending,
		ResultContract:      sql.NullString{String: "alert_payload.v1", Valid: true},
		NotificationRuleSet: sql.NullString{String: "default", Valid: true},
		DefaultDestinations: pq.StringArray{"telegram"},
	}
	err = taskRepo.CreateTask(context.Background(), task)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM tasks WHERE id = $1", task.ID)

	// 3. Get Pending Task
	tasks, err := taskRepo.GetPendingTasksByAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, taskID, tasks[0].ID)
	require.Equal(t, "alert_payload.v1", tasks[0].ResultContract.String)

	scheduledTask := &Task{
		ID:           uuid.New(),
		AgentID:      agent.ID,
		TaskType:     TaskTypeExecCommand,
		Status:       TaskStatusPending,
		ScheduleType: sql.NullString{String: "ONCE", Valid: true},
		ScheduledAt:  sql.NullTime{Time: time.Now().UTC().Add(time.Hour), Valid: true},
	}
	err = taskRepo.CreateTask(context.Background(), scheduledTask)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM tasks WHERE id = $1", scheduledTask.ID)

	chainedTask := &Task{
		ID:                 uuid.New(),
		AgentID:            agent.ID,
		TaskType:           TaskTypeExecCommand,
		Status:             TaskStatusPending,
		ScheduleType:       sql.NullString{String: "CHAINED", Valid: true},
		PrerequisiteTaskID: uuid.NullUUID{UUID: taskID, Valid: true},
	}
	err = taskRepo.CreateTask(context.Background(), chainedTask)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM tasks WHERE id = $1", chainedTask.ID)

	tasks, err = taskRepo.GetPendingTasksByAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, taskID, tasks[0].ID)

	scheduledTasks, err := taskRepo.GetScheduledTasks(context.Background())
	require.NoError(t, err)
	require.Len(t, scheduledTasks, 1)
	require.Equal(t, scheduledTask.ID, scheduledTasks[0].ID)

	// 4. Update Task Status
	err = taskRepo.UpdateTaskStatus(context.Background(), taskID, TaskStatusAssigned)
	require.NoError(t, err)
	var updatedTask Task
	err = s.db.Get(&updatedTask, "SELECT * FROM tasks WHERE id=$1", taskID)
	require.NoError(t, err)
	require.Equal(t, TaskStatusAssigned, updatedTask.Status)

	// 5. Create Task Result
	result := &TaskResult{
		ID:      uuid.New(),
		TaskID:  taskID,
		AgentID: agent.ID,
		Status:  TaskResultStatusSuccess,
	}
	err = taskRepo.CreateTaskResult(context.Background(), result)
	require.NoError(t, err)
	var count int
	err = s.db.Get(&count, "SELECT COUNT(*) FROM task_results WHERE task_id=$1", taskID)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func (s *StorageTestSuite) TestLogRepository() {
	t := s.T()
	logRepo := NewPostgresLogRepository(s.db)
	agentRepo := NewPostgresAgentRepository(s.db)

	// 1. Create agent and task for the test
	agent := &Agent{
		ID:       uuid.New(),
		Hostname: "log-test-agent",
		Status:   AgentStatusOnline,
	}
	err := agentRepo.CreateAgent(context.Background(), agent)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM agents WHERE id = $1", agent.ID)

	task := &Task{ID: uuid.New(), AgentID: agent.ID, TaskType: TaskTypeExecCommand, Status: TaskStatusRunning}
	_, err = s.db.Exec("INSERT INTO tasks (id, agent_id, task_type, status) VALUES ($1, $2, $3, $4)", task.ID, task.AgentID, task.TaskType, task.Status)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM tasks WHERE id = $1", task.ID)

	// 2. Create Log Entries
	logs := []*Log{
		{ID: uuid.New(), AgentID: agent.ID, TaskID: task.ID, Timestamp: time.Now(), Level: LogLevelInfo, Message: "Log 1"},
		{ID: uuid.New(), AgentID: agent.ID, TaskID: task.ID, Timestamp: time.Now(), Level: LogLevelError, Message: "Log 2"},
	}

	err = logRepo.CreateLogEntries(context.Background(), logs)
	require.NoError(t, err)

	var count int
	err = s.db.Get(&count, "SELECT COUNT(*) FROM logs WHERE task_id=$1", task.ID)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func (s *StorageTestSuite) TestNotificationRepository() {
	t := s.T()
	notificationRepo := NewPostgresNotificationRepository(s.db)
	agentRepo := NewPostgresAgentRepository(s.db)
	taskRepo := NewPostgresTaskRepository(s.db)

	agent := &Agent{
		ID:       uuid.New(),
		Hostname: "notification-test-agent",
		Status:   AgentStatusOnline,
	}
	err := agentRepo.CreateAgent(context.Background(), agent)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM agents WHERE id = $1", agent.ID)

	task := &Task{
		ID:       uuid.New(),
		AgentID:  agent.ID,
		TaskType: TaskTypeExecPythonScript,
		Status:   TaskStatusCompleted,
	}
	err = taskRepo.CreateTask(context.Background(), task)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM tasks WHERE id = $1", task.ID)

	event := &NotificationEvent{
		ID:                 uuid.New(),
		TaskID:             task.ID,
		AgentID:            agent.ID,
		PrerequisiteTaskID: uuid.NullUUID{},
		EventType:          "file.lines_detected",
		Severity:           "warning",
		Title:              "Matches found",
		Summary:            "Found 2 lines",
		SourceKind:         "file",
		SourcePath:         sql.NullString{String: "result.txt", Valid: true},
		SourceRef:          sql.NullString{},
		PayloadJSON:        []byte(`{"schema":"alert_payload","version":"1"}`),
		DedupKey:           sql.NullString{String: "sha256:test", Valid: true},
		DedupWindowSeconds: sql.NullInt32{Int32: 3600, Valid: true},
		Status:             NotificationEventStatusDetected,
	}
	err = notificationRepo.CreateNotificationEvent(context.Background(), event)
	require.NoError(t, err)

	storedEvent, err := notificationRepo.GetNotificationEventByID(context.Background(), event.ID)
	require.NoError(t, err)
	require.Equal(t, event.EventType, storedEvent.EventType)
	require.Equal(t, event.Status, storedEvent.Status)

	latestEvent, err := notificationRepo.FindLatestNotificationEventByDedupKeySince(context.Background(), "sha256:test", time.Now().UTC().Add(-time.Hour), uuid.Nil)
	require.NoError(t, err)
	require.Equal(t, event.ID, latestEvent.ID)

	events, err := notificationRepo.ListNotificationEvents(context.Background(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, events)

	delivery := &NotificationDelivery{
		ID:                   uuid.New(),
		NotificationEventID:  event.ID,
		Channel:              "telegram",
		Destination:          "default",
		Status:               NotificationDeliveryStatusPending,
		Attempt:              1,
		MaxAttempts:          5,
		ProviderMessageID:    sql.NullString{},
		ProviderResponseJSON: nil,
		ErrorMessage:         sql.NullString{},
		LastErrorCode:        sql.NullString{},
		ScheduledAt:          time.Now().UTC(),
		SentAt:               sql.NullTime{},
		NextRetryAt:          sql.NullTime{},
	}
	err = notificationRepo.CreateNotificationDelivery(context.Background(), delivery)
	require.NoError(t, err)

	err = notificationRepo.UpdateNotificationDeliveryStatus(
		context.Background(),
		delivery.ID,
		2,
		NotificationDeliveryStatusRetryScheduled,
		sql.NullString{},
		[]byte(`{"provider":"telegram"}`),
		sql.NullString{String: "temporary failure", Valid: true},
		sql.NullString{String: "TEMP_ERROR", Valid: true},
		sql.NullTime{},
		sql.NullTime{Time: time.Now().UTC().Add(5 * time.Minute), Valid: true},
	)
	require.NoError(t, err)

	deliveries, err := notificationRepo.ListNotificationDeliveriesByEventID(context.Background(), event.ID)
	require.NoError(t, err)
	require.Len(t, deliveries, 1)
	require.Equal(t, NotificationDeliveryStatusRetryScheduled, deliveries[0].Status)
	require.Equal(t, int32(2), deliveries[0].Attempt)

	dueDeliveries, err := notificationRepo.ListNotificationDeliveriesForDispatch(context.Background(), time.Now().UTC().Add(10*time.Minute), 10)
	require.NoError(t, err)
	require.NotEmpty(t, dueDeliveries)
}
