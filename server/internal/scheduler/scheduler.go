package scheduler

import (
	"agent-management/server/internal/storage"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// Scheduler is responsible for managing and running scheduled tasks.
type Scheduler struct {
	logger    *slog.Logger
	taskRepo  storage.TaskRepository
	taskQueue chan uuid.UUID
	cron      *cron.Cron
	tasks     map[uuid.UUID]storage.Task
	ctx       context.Context
	cancel    context.CancelFunc
}

const schedulerRefreshInterval = 15 * time.Second

// NewScheduler creates a new scheduler.
func NewScheduler(logger *slog.Logger, taskRepo storage.TaskRepository, taskQueue chan uuid.UUID) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		logger:    logger,
		taskRepo:  taskRepo,
		taskQueue: taskQueue,
		cron:      cron.New(),
		tasks:     make(map[uuid.UUID]storage.Task),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start begins the scheduler's background processing.
func (s *Scheduler) Start() error {
	s.logger.Info("Starting scheduler...")

	if err := s.loadScheduledTasks(s.ctx); err != nil {
		return err
	}
	s.checkAndQueueDueTasks()

	s.cron.Start()

	go func() {
		ticker := time.NewTicker(schedulerRefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.loadScheduledTasks(s.ctx); err != nil {
					s.logger.Error("Failed to refresh scheduled tasks", "error", err)
				}
				s.checkAndQueueDueTasks()
			case <-s.ctx.Done():
				s.logger.Info("Scheduler stopping.")
				s.cron.Stop()
				return
			}
		}
	}()

	return nil
}

// Stop gracefully shuts down the scheduler.
func (s *Scheduler) Stop() {
	s.cancel()
}

func (s *Scheduler) add(task storage.Task) {
	s.tasks[task.ID] = task
}

func (s *Scheduler) loadScheduledTasks(ctx context.Context) error {
	s.logger.Info("Loading scheduled tasks from database...")
	tasks, err := s.taskRepo.GetScheduledTasks(ctx)
	if err != nil {
		s.logger.Error("Failed to load scheduled tasks", "error", err)
		return err
	}

	for _, task := range tasks {
		switch task.ScheduleType.String {
		case "ONCE":
			s.add(task)
		case "RECURRING":
			if _, exists := s.tasks[task.ID]; exists {
				continue
			}
			if _, err := s.AddRecurring(task); err != nil {
				s.logger.Error("Failed to register recurring task", "task_id", task.ID, "error", err)
			}
		}
	}
	s.logger.Info("Finished loading scheduled tasks", "count", len(tasks))
	return nil
}

func (s *Scheduler) checkAndQueueDueTasks() {
	s.logger.Debug("Checking for due tasks...")
	now := time.Now()

	for id, task := range s.tasks {
		if task.Status != storage.TaskStatusPending {
			continue // Skip non-pending tasks
		}

		if task.ScheduleType.String == "ONCE" && task.ScheduledAt.Valid && !task.ScheduledAt.Time.After(now) {
			s.logger.Info("Queuing one-time task", "task_id", id)
			s.taskQueue <- id
			delete(s.tasks, id)
		}
	}
}

func (s *Scheduler) AddRecurring(task storage.Task) (cron.EntryID, error) {
	if task.ScheduleType.String != "RECURRING" || !task.CronExpression.Valid {
		return 0, errors.New("task is not a valid recurring task")
	}

	s.logger.Info("Adding recurring task", "task_id", task.ID, "cron", task.CronExpression.String)
	id, err := s.cron.AddFunc(task.CronExpression.String, func() {
		s.logger.Info("Queuing recurring task", "task_id", task.ID)
		s.taskQueue <- task.ID
	})
	if err != nil {
		s.logger.Error("Failed to add recurring task to cron", "task_id", task.ID, "error", err)
		return 0, err
	}
	s.add(task)
	return id, nil
}
