package internal

import (
	"agent-management/server/internal/events"
	"agent-management/server/internal/notifier"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"agent-management/pkg/api"
	"agent-management/server/internal/storage"
	"agent-management/server/internal/storage/mocks"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

func setupTestServer(t *testing.T) (*Server, *mocks.MockAgentRepository, *mocks.MockTaskRepository, *mocks.MockLogRepository, *mocks.MockMetricRepository, chan uuid.UUID, *events.EventBroker) {
	ctrl := gomock.NewController(t)
	mockAgentRepo := mocks.NewMockAgentRepository(ctrl)
	mockTaskRepo := mocks.NewMockTaskRepository(ctrl)
	mockLogRepo := mocks.NewMockLogRepository(ctrl)
	mockMetricRepo := mocks.NewMockMetricRepository(ctrl)

	mockStorage := &storage.Storage{
		Agent:  mockAgentRepo,
		Task:   mockTaskRepo,
		Log:    mockLogRepo,
		Metric: mockMetricRepo,
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)

	taskQueue := make(chan uuid.UUID, 100)

	broker := events.NewEventBroker(logger)
	go broker.Start()
	t.Cleanup(broker.Stop)

	s := NewServer(logger, mockStorage, multiNotifier, taskQueue, broker)
	return s, mockAgentRepo, mockTaskRepo, mockLogRepo, mockMetricRepo, taskQueue, broker
}

func TestRegisterAgent(t *testing.T) {
	s, mockAgentRepo, _, _, _, _, _ := setupTestServer(t)

	agentID := uuid.New()
	req := &api.RegisterAgentRequest{
		AgentId:      agentID.String(),
		Hostname:     "test-host",
		Os:           "windows",
		Arch:         "amd64",
		AgentVersion: "0.0.1",
		IpAddresses:  []string{"127.0.0.1"},
	}

	t.Run("Successful registration", func(t *testing.T) {
		mockAgentRepo.EXPECT().
			CreateAgent(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, agent *storage.Agent) error {
				if agent.ID != agentID {
					t.Errorf("expected agent ID %v, got %v", agentID, agent.ID)
				}
				return nil
			}).Times(1)

		res, err := s.RegisterAgent(context.Background(), req)
		if err != nil {
			t.Fatalf("RegisterAgent failed: %v", err)
		}
		if !res.Success {
			t.Errorf("RegisterAgent returned success=false")
		}
	})

	t.Run("Invalid UUID", func(t *testing.T) {
		invalidReq := &api.RegisterAgentRequest{AgentId: "not-a-uuid"}
		res, err := s.RegisterAgent(context.Background(), invalidReq)
		if err != nil {
			t.Fatalf("RegisterAgent with invalid UUID failed unexpectedly: %v", err)
		}
		if res.Success {
			t.Errorf("RegisterAgent with invalid UUID should have returned success=false")
		}
	})

	t.Run("Database error", func(t *testing.T) {
		dbError := errors.New("database error")
		mockAgentRepo.EXPECT().
			CreateAgent(gomock.Any(), gomock.Any()).
			Return(dbError).
			Times(1)

		res, err := s.RegisterAgent(context.Background(), req)
		if err != nil {
			t.Fatalf("RegisterAgent with DB error failed unexpectedly: %v", err)
		}
		if res.Success {
			t.Errorf("RegisterAgent with DB error should have returned success=false")
		}
	})
}

func TestSendHeartbeat(t *testing.T) {
	s, mockAgentRepo, _, _, mockMetricRepo, _, _ := setupTestServer(t)
	agentID := uuid.New()

	req := &api.HeartbeatRequest{
		AgentId: agentID.String(),
	}

	t.Run("Successful heartbeat", func(t *testing.T) {
		mockAgentRepo.EXPECT().
			UpdateAgentStatus(gomock.Any(), agentID, storage.AgentStatusOnline, gomock.Any()).
			Return(nil).
			Times(1)
		mockMetricRepo.EXPECT().
			StoreAgentMetric(gomock.Any(), gomock.Any()).
			Return(nil).
			AnyTimes()

		res, err := s.SendHeartbeat(context.Background(), req)
		if err != nil {
			t.Fatalf("SendHeartbeat failed: %v", err)
		}
		if !res.Success {
			t.Errorf("SendHeartbeat returned success=false")
		}
	})

	t.Run("Database error on heartbeat", func(t *testing.T) {
		dbError := errors.New("db error")
		mockAgentRepo.EXPECT().
			UpdateAgentStatus(gomock.Any(), agentID, storage.AgentStatusOnline, gomock.Any()).
			Return(dbError).
			Times(1)
		mockMetricRepo.EXPECT().
			StoreAgentMetric(gomock.Any(), gomock.Any()).
			Return(nil).
			AnyTimes()

		res, err := s.SendHeartbeat(context.Background(), req)
		if err != nil {
			t.Fatalf("SendHeartbeat with DB error failed unexpectedly: %v", err)
		}
		if !res.Success {
			t.Errorf("SendHeartbeat with DB error should have returned success=true")
		}
	})
}

func TestSubmitTaskResult(t *testing.T) {
	s, _, mockTaskRepo, _, _, taskQueue, _ := setupTestServer(t)

	agentID := uuid.New()
	taskID := uuid.New()

	req := &api.SubmitTaskResultRequest{
		AgentId:    agentID.String(),
		TaskId:     taskID.String(),
		Status:     api.TaskStatus_SUCCESS,
		ExitCode:   0,
		Output:     "Completed successfully",
		DurationMs: 1234,
	}

	t.Run("Successful result submission", func(t *testing.T) {
		gomock.InOrder(
			mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&storage.Task{ID: taskID}, nil),
			mockTaskRepo.EXPECT().CreateTaskResult(gomock.Any(), gomock.Any()).Return(nil),
			mockTaskRepo.EXPECT().UpdateTaskStatus(gomock.Any(), taskID, storage.TaskStatusCompleted).Return(nil),
			mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&storage.Task{ID: taskID}, nil), // For broadcast
			mockTaskRepo.EXPECT().GetTasksByPrerequisite(gomock.Any(), taskID).Return([]storage.Task{}, nil),
		)

		res, err := s.SubmitTaskResult(context.Background(), req)
		if err != nil {
			t.Fatalf("SubmitTaskResult failed: %v", err)
		}
		if !res.Success {
			t.Errorf("SubmitTaskResult returned success=false")
		}
	})

	t.Run("Triggers chained task on success", func(t *testing.T) {
		chainedTaskID := uuid.New()
		chainedTask := storage.Task{ID: chainedTaskID}

		gomock.InOrder(
			mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&storage.Task{ID: taskID}, nil),
			mockTaskRepo.EXPECT().CreateTaskResult(gomock.Any(), gomock.Any()).Return(nil),
			mockTaskRepo.EXPECT().UpdateTaskStatus(gomock.Any(), taskID, storage.TaskStatusCompleted).Return(nil),
			mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&storage.Task{ID: taskID}, nil), // For broadcast
			mockTaskRepo.EXPECT().GetTasksByPrerequisite(gomock.Any(), taskID).Return([]storage.Task{chainedTask}, nil),
		)

		_, err := s.SubmitTaskResult(context.Background(), req)
		if err != nil {
			t.Fatalf("SubmitTaskResult failed: %v", err)
		}

		select {
		case queuedID := <-taskQueue:
			if queuedID != chainedTaskID {
				t.Errorf("expected chained task ID %v to be queued, but got %v", chainedTaskID, queuedID)
			}
		case <-time.After(1 * time.Second):
			t.Fatal("timed out waiting for chained task to be queued")
		}
	})

	t.Run("CreateTaskResult DB error", func(t *testing.T) {
		dbError := errors.New("db error on create")
		mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&storage.Task{ID: taskID}, nil)
		mockTaskRepo.EXPECT().CreateTaskResult(gomock.Any(), gomock.Any()).Return(dbError)
		// Other calls should not be made
		mockTaskRepo.EXPECT().UpdateTaskStatus(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

		res, err := s.SubmitTaskResult(context.Background(), req)
		if err != nil {
			t.Fatalf("SubmitTaskResult with DB error failed unexpectedly: %v", err)
		}
		if res.Success {
			t.Errorf("SubmitTaskResult with DB error should have returned success=false")
		}
	})

	t.Run("UpdateTaskStatus DB error", func(t *testing.T) {
		dbError := errors.New("db error on update")
		gomock.InOrder(
			mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&storage.Task{ID: taskID}, nil),
			mockTaskRepo.EXPECT().CreateTaskResult(gomock.Any(), gomock.Any()).Return(nil),
			mockTaskRepo.EXPECT().UpdateTaskStatus(gomock.Any(), taskID, storage.TaskStatusCompleted).Return(dbError),
		)

		res, err := s.SubmitTaskResult(context.Background(), req)
		if err != nil {
			t.Fatalf("SubmitTaskResult with DB error failed unexpectedly: %v", err)
		}
		if res.Success {
			t.Errorf("SubmitTaskResult with DB error should have returned success=false")
		}
	})
}
