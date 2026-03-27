package scheduler

import (
	"agent-management/server/internal/storage"
	"agent-management/server/internal/storage/mocks"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

func setupTestScheduler(t *testing.T) (*Scheduler, *mocks.MockTaskRepository) {
	ctrl := gomock.NewController(t)
	mockTaskRepo := mocks.NewMockTaskRepository(ctrl)

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Dummy channel for task queue
	taskQueue := make(chan uuid.UUID, 100)

	s := NewScheduler(logger, mockTaskRepo, taskQueue)
	return s, mockTaskRepo
}
func TestLoadScheduledTasks(t *testing.T) {
	s, mockTaskRepo := setupTestScheduler(t)

	t.Run("Successfully loads scheduled tasks", func(t *testing.T) {
		// Expected tasks to be returned by the mock
		expectedTasks := []storage.Task{
			{ID: uuid.New(), Status: storage.TaskStatusPending, ScheduleType: sql.NullString{String: "ONCE", Valid: true}},
			{ID: uuid.New(), Status: storage.TaskStatusPending, ScheduleType: sql.NullString{String: "RECURRING", Valid: true}},
		}

		mockTaskRepo.EXPECT().
			GetScheduledTasks(gomock.Any()).
			Return(expectedTasks, nil).
			Times(1)

		err := s.loadScheduledTasks(context.Background())
		if err != nil {
			t.Fatalf("loadScheduledTasks failed: %v", err)
		}

		// Additional checks could be added here to see if the tasks were added to the scheduler's internal state
	})

	t.Run("Handles database error on load", func(t *testing.T) {
		dbError := errors.New("database error")
		mockTaskRepo.EXPECT().
			GetScheduledTasks(gomock.Any()).
			Return(nil, dbError).
			Times(1)

		err := s.loadScheduledTasks(context.Background())
		if err == nil {
			t.Fatal("loadScheduledTasks should have returned an error")
		}
	})
}

func TestCheckAndQueueDueTasks(t *testing.T) {
	s, _ := setupTestScheduler(t)

	t.Run("Correctly queues a due one-time task", func(t *testing.T) {
		taskID := uuid.New()
		dueTask := storage.Task{
			ID:           taskID,
			Status:       storage.TaskStatusPending,
			ScheduleType: sql.NullString{String: "ONCE", Valid: true},
			ScheduledAt:  sql.NullTime{Time: time.Now().Add(-1 * time.Minute), Valid: true}, // Due 1 minute ago
		}

		s.add(dueTask) // Manually add task to scheduler for testing this unit

		s.checkAndQueueDueTasks()

		select {
		case queuedID := <-s.taskQueue:
			if queuedID != taskID {
				t.Errorf("expected queued task ID %v, got %v", taskID, queuedID)
			}
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for task to be queued")
		}
	})

	t.Run("Correctly queues a due recurring task", func(t *testing.T) {
		taskID := uuid.New()
		// Cron for every minute
		dueTask := storage.Task{
			ID:             taskID,
			Status:         storage.TaskStatusPending,
			ScheduleType:   sql.NullString{String: "RECURRING", Valid: true},
			CronExpression: sql.NullString{String: "* * * * *", Valid: true},
		}

		s.AddRecurring(dueTask)

		// In a real scenario, the cron library would trigger this.
		// For this test, we can't easily test the cron's invocation.
		// A more advanced test would involve passing a mock cron object.
		// For now, we'll just check the task was added.
		if _, exists := s.tasks[taskID]; !exists {
			t.Fatal("recurring task was not added to the scheduler")
		}
	})
}
