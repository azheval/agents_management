package storage

import (
	"context"
	"log"
	"testing"
	"time"

	"agent-management/server/internal/config"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
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
		ID:       taskID,
		AgentID:  agent.ID,
		TaskType: TaskTypeExecCommand,
		Status:   TaskStatusPending,
	}
	err = taskRepo.CreateTask(context.Background(), task)
	require.NoError(t, err)
	defer s.db.Exec("DELETE FROM tasks WHERE id = $1", task.ID)

	// 3. Get Pending Task
	tasks, err := taskRepo.GetPendingTasksByAgent(context.Background(), agent.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, taskID, tasks[0].ID)

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
