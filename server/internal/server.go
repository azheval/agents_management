package internal

import (
	"agent-management/server/internal/events"
	"agent-management/server/internal/notifier"
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"agent-management/pkg/api"
	"agent-management/server/internal/config"
	"agent-management/server/internal/storage"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Server is used to implement agent.AgentServiceServer.
type Server struct {
	api.UnimplementedAgentServiceServer
	api.UnimplementedTaskServiceServer
	logger                         *slog.Logger
	storage                        *storage.Storage
	notifier                       *notifier.MultiNotifier
	taskQueue                      chan uuid.UUID
	broker                         *events.EventBroker
	notificationRuleEngine         *NotificationRuleEngine
	notificationRouter             *NotificationRouter
	notificationDeliveryDispatcher *NotificationDeliveryDispatcher
}

func NewServer(logger *slog.Logger, storage *storage.Storage, notifier *notifier.MultiNotifier, taskQueue chan uuid.UUID, broker *events.EventBroker) *Server {
	return &Server{
		logger:    logger,
		storage:   storage,
		notifier:  notifier,
		taskQueue: taskQueue,
		broker:    broker,
	}
}

func (s *Server) SetNotificationPipeline(ruleEngine *NotificationRuleEngine, router *NotificationRouter) {
	s.notificationRuleEngine = ruleEngine
	s.notificationRouter = router
}

func (s *Server) SetNotificationDeliveryDispatcher(dispatcher *NotificationDeliveryDispatcher) {
	s.notificationDeliveryDispatcher = dispatcher
}

func (s *Server) RegisterAgent(ctx context.Context, in *api.RegisterAgentRequest) (*api.RegisterAgentResponse, error) {
	s.logger.Info("Received RegisterAgent request", "hostname", in.GetHostname(), "agent_id", in.GetAgentId())

	agentID, err := uuid.Parse(in.GetAgentId())
	if err != nil {
		s.logger.Error("Failed to parse agent ID", "agent_id", in.GetAgentId(), "error", err)
		return &api.RegisterAgentResponse{Success: false, Message: "Invalid agent ID format"}, nil
	}

	agent := &storage.Agent{
		ID:            agentID,
		Hostname:      in.GetHostname(),
		OS:            sql.NullString{String: in.GetOs(), Valid: in.GetOs() != ""},
		Arch:          sql.NullString{String: in.GetArch(), Valid: in.GetArch() != ""},
		AgentVersion:  sql.NullString{String: in.GetAgentVersion(), Valid: in.GetAgentVersion() != ""},
		IPAddresses:   pq.StringArray(in.GetIpAddresses()),
		Status:        storage.AgentStatusOnline,
		LastHeartbeat: sql.NullTime{Time: time.Now(), Valid: true},
	}

	if err := s.storage.Agent.CreateAgent(ctx, agent); err != nil {
		s.logger.Error("Failed to register agent in db", "agent_id", in.GetAgentId(), "error", err)
		return &api.RegisterAgentResponse{Success: false, Message: "Failed to register agent"}, nil
	}

	s.logger.Info("Agent registered successfully", "agent_id", in.GetAgentId())
	return &api.RegisterAgentResponse{Success: true, Message: "Agent registered successfully"}, nil
}

func (s *Server) SendHeartbeat(ctx context.Context, in *api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
	s.logger.Debug("Received Heartbeat", "agent_id", in.GetAgentId())

	agentID, err := uuid.Parse(in.GetAgentId())
	if err != nil {
		s.logger.Error("Failed to parse agent ID for heartbeat", "agent_id", in.GetAgentId(), "error", err)
		return &api.HeartbeatResponse{Success: false, Message: "Invalid agent ID format"}, nil
	}

	// The primary goal is to update the agent's status.
	// We do this first. Storing metrics is secondary.
	heartbeatTime := time.Now()
	err = s.storage.Agent.UpdateAgentStatus(ctx, agentID, storage.AgentStatusOnline, heartbeatTime)
	if err != nil {
		s.logger.Error("Failed to update agent status on heartbeat", "agent_id", in.GetAgentId(), "error", err)
		// We still try to save metrics even if status update fails.
	}

	// Now, process and store the metrics if they are present.
	// We'll log errors but won't fail the heartbeat if metric storage fails.
	if in.GetCpuUsage() > 0 || in.GetRamUsage() > 0 {
		var diskUsageJSON []byte
		if len(in.GetDiskUsage()) > 0 {
			diskUsageJSON, err = json.Marshal(in.GetDiskUsage())
			if err != nil {
				s.logger.Error("Failed to marshal disk usage to JSON", "agent_id", in.GetAgentId(), "error", err)
			}
		}

		metric := &storage.AgentMetric{
			AgentID:   agentID,
			Timestamp: time.Unix(in.GetTimestamp(), 0),
			CPUUsage:  in.GetCpuUsage(),
			RAMUsage:  in.GetRamUsage(),
			DiskUsage: diskUsageJSON,
		}

		if err := s.storage.Metric.StoreAgentMetric(ctx, metric); err != nil {
			s.logger.Error("Failed to store agent metrics", "agent_id", in.GetAgentId(), "error", err)
		} else {
			s.logger.Debug("Successfully stored agent metrics", "agent_id", in.GetAgentId())
		}
	}

	return &api.HeartbeatResponse{Success: true, Message: "Heartbeat received"}, nil
}

// StreamLogs implements the client-streaming RPC for receiving logs from an agent.
func (s *Server) StreamLogs(stream api.AgentService_StreamLogsServer) error {
	s.logger.Info("Agent connected for log streaming.")
	var logBuffer []*storage.Log

	for {
		logEntry, err := stream.Recv()
		if err == io.EOF {
			if len(logBuffer) > 0 {
				s.logger.Info("Flushing remaining logs", "count", len(logBuffer))
				if err := s.storage.Log.CreateLogEntries(stream.Context(), logBuffer); err != nil {
					s.logger.Error("Failed to save final log batch", "error", err)
				}
			}
			s.logger.Info("Finished receiving logs from stream.")
			return stream.SendAndClose(&api.StreamLogsResponse{Success: true, Message: "Logs received"})
		}
		if err != nil {
			s.logger.Error("Error receiving log entry from stream", "error", err)
			return err
		}

		agentID, err := uuid.Parse(logEntry.GetAgentId())
		if err != nil {
			s.logger.Warn("Received log entry with invalid agent ID, skipping", "agent_id", logEntry.GetAgentId())
			continue
		}
		taskID, err := uuid.Parse(logEntry.GetTaskId())
		if err != nil {
			s.logger.Warn("Received log entry with invalid task ID, skipping", "task_id", logEntry.GetTaskId())
			continue
		}

		log := &storage.Log{
			ID:        uuid.New(),
			AgentID:   agentID,
			TaskID:    taskID,
			Timestamp: time.Unix(logEntry.GetTimestamp(), 0),
			Level:     storage.LogLevel(logEntry.GetLevel()),
			Message:   logEntry.GetMessage(),
		}
		logBuffer = append(logBuffer, log)

		if len(logBuffer) >= 100 {
			s.logger.Info("Flushing log buffer to database", "count", len(logBuffer))
			if err := s.storage.Log.CreateLogEntries(stream.Context(), logBuffer); err != nil {
				s.logger.Error("Failed to save log batch", "error", err)
			}
			logBuffer = nil
		}
	}
}

// SubmitTaskResult implements the unary RPC for receiving the final result of a task.
func (s *Server) SubmitTaskResult(ctx context.Context, in *api.SubmitTaskResultRequest) (*api.SubmitTaskResultResponse, error) {
	s.logger.Info("Received SubmitTaskResult request", "task_id", in.GetTaskId())

	agentID, err := uuid.Parse(in.GetAgentId())
	if err != nil {
		s.logger.Error("Failed to parse agent ID for task result", "agent_id", in.GetAgentId(), "error", err)
		return &api.SubmitTaskResultResponse{Success: false, Message: "Invalid agent ID format"}, nil
	}
	taskID, err := uuid.Parse(in.GetTaskId())
	if err != nil {
		s.logger.Error("Failed to parse task ID for task result", "task_id", in.GetTaskId(), "error", err)
		return &api.SubmitTaskResultResponse{Success: false, Message: "Invalid task ID format"}, nil
	}

	task, err := s.storage.Task.GetTaskByID(ctx, taskID)
	if err != nil {
		s.logger.Error("Failed to get task details for task result", "task_id", taskID, "error", err)
		return &api.SubmitTaskResultResponse{Success: false, Message: "Task not found"}, nil
	}

	result := &storage.TaskResult{
		ID:         uuid.New(),
		TaskID:     taskID,
		AgentID:    agentID,
		Status:     storage.TaskResultStatus(in.GetStatus().String()),
		ExitCode:   sql.NullInt32{Int32: in.GetExitCode(), Valid: true},
		Output:     sql.NullString{String: in.GetOutput(), Valid: true},
		DurationMs: sql.NullInt64{Int64: in.GetDurationMs(), Valid: true},
	}
	if task.TaskType == storage.TaskTypeFetchFile && in.GetStatus() == api.TaskStatus_SUCCESS && task.DestinationPath.Valid {
		result.OutputFilePath = task.DestinationPath
	}

	if err := s.storage.Task.CreateTaskResult(ctx, result); err != nil {
		s.logger.Error("Failed to save task result", "task_id", taskID, "error", err)
		return &api.SubmitTaskResultResponse{Success: false, Message: "Failed to save task result"}, nil
	}

	var finalTaskStatus storage.TaskStatus
	switch in.GetStatus() {
	case api.TaskStatus_SUCCESS:
		finalTaskStatus = storage.TaskStatusCompleted
	case api.TaskStatus_FAILED:
		finalTaskStatus = storage.TaskStatusFailed
	case api.TaskStatus_TIMED_OUT:
		finalTaskStatus = storage.TaskStatusTimedOut
	default:
		s.logger.Warn("Unknown task status in result submission", "status", in.GetStatus())
		finalTaskStatus = storage.TaskStatusFailed
	}

	persistedTaskStatus := finalTaskStatus
	if task.ScheduleType.Valid && task.ScheduleType.String == "RECURRING" {
		persistedTaskStatus = storage.TaskStatusPending
	}

	if err := s.storage.Task.MarkTaskCompleted(ctx, taskID, persistedTaskStatus, time.Now().UTC()); err != nil {
		s.logger.Error("Failed to update final task status", "task_id", taskID, "error", err)
		return &api.SubmitTaskResultResponse{Success: false, Message: "Failed to update final task status"}, nil
	}

	s.evaluateAndStoreNotificationEvent(ctx, task, result)

	updatedTask, err := s.storage.Task.GetTaskByID(ctx, taskID)
	if err != nil {
		s.logger.Error("Failed to get updated task for event publishing", "task_id", taskID, "error", err)
		// Don't fail the RPC, just log the error
	} else {
		s.broker.Broadcast(updatedTask)
	}

	// If the task was successful, check for and trigger any chained tasks.
	if finalTaskStatus == storage.TaskStatusCompleted {
		s.triggerChainedTasks(ctx, taskID)
	}

	// --- Send Notification and Cleanup ---
	go s.notifier.NotifyTaskCompletion(task, result)

	if len(task.PackageFiles) > 0 {
		go func() {
			uploadDir := filepath.Dir(task.PackageFiles[0])
			s.logger.Info("Cleaning up task upload directory", "dir", uploadDir)
			if err := os.RemoveAll(uploadDir); err != nil {
				s.logger.Error("Failed to clean up task upload directory", "dir", uploadDir, "error", err)
			}
		}()
	}

	s.logger.Info("Successfully processed task result", "task_id", taskID)
	return &api.SubmitTaskResultResponse{Success: true, Message: "Result processed successfully"}, nil
}

func (s *Server) UploadTaskFile(stream api.AgentService_UploadTaskFileServer) error {
	ctx := stream.Context()
	var (
		agentID uuid.UUID
		taskID  uuid.UUID
		task    *storage.Task
		outFile *os.File
		written int64
	)

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			if outFile != nil {
				if closeErr := outFile.Close(); closeErr != nil {
					s.logger.Error("Failed to close uploaded file", "task_id", taskID, "error", closeErr)
					return closeErr
				}
			}
			return stream.SendAndClose(&api.UploadTaskFileResponse{
				Success:       true,
				Message:       "File uploaded",
				BytesReceived: written,
			})
		}
		if err != nil {
			return err
		}

		if task == nil {
			agentID, err = uuid.Parse(req.GetAgentId())
			if err != nil {
				return status.Errorf(codes.InvalidArgument, "Invalid agent ID format")
			}
			taskID, err = uuid.Parse(req.GetTaskId())
			if err != nil {
				return status.Errorf(codes.InvalidArgument, "Invalid task ID format")
			}

			task, err = s.storage.Task.GetTaskByID(ctx, taskID)
			if err != nil {
				return status.Errorf(codes.NotFound, "Task not found")
			}
			if task.AgentID != agentID {
				return status.Errorf(codes.PermissionDenied, "Task does not belong to agent")
			}
			if task.TaskType != storage.TaskTypeFetchFile {
				return status.Errorf(codes.FailedPrecondition, "Task does not accept uploaded files")
			}
			if !task.DestinationPath.Valid || task.DestinationPath.String == "" {
				return status.Errorf(codes.InvalidArgument, "Task destination path is empty")
			}

			if err := os.MkdirAll(filepath.Dir(task.DestinationPath.String), os.ModePerm); err != nil {
				return status.Errorf(codes.Internal, "Failed to create destination directory")
			}
			outFile, err = os.OpenFile(task.DestinationPath.String, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
			if err != nil {
				return status.Errorf(codes.Internal, "Failed to open destination file")
			}
		}

		n, err := outFile.Write(req.GetContent())
		written += int64(n)
		if err != nil {
			outFile.Close()
			return status.Errorf(codes.Internal, "Failed to write uploaded file")
		}
	}
}

func (s *Server) triggerChainedTasks(ctx context.Context, prerequisiteTaskID uuid.UUID) {
	s.logger.Info("Checking for chained tasks", "prerequisite_task_id", prerequisiteTaskID)
	chainedTasks, err := s.storage.Task.GetTasksByPrerequisite(ctx, prerequisiteTaskID)
	if err != nil {
		s.logger.Error("Failed to get chained tasks", "prerequisite_task_id", prerequisiteTaskID, "error", err)
		return
	}

	if len(chainedTasks) == 0 {
		s.logger.Info("No chained tasks found", "prerequisite_task_id", prerequisiteTaskID)
		return
	}

	for _, task := range chainedTasks {
		s.logger.Info("Triggering chained task", "task_id", task.ID, "prerequisite_task_id", prerequisiteTaskID)
		s.taskQueue <- task.ID
	}
}

func mapStorageTaskType(taskType storage.TaskType) (api.TaskType, bool) {
	switch taskType {
	case storage.TaskTypeExecCommand:
		return api.TaskType_EXEC_COMMAND, true
	case storage.TaskTypeExecPythonScript:
		return api.TaskType_EXEC_PYTHON_SCRIPT, true
	case storage.TaskTypeFetchFile:
		return api.TaskType_FETCH_FILE, true
	case storage.TaskTypePushFile:
		return api.TaskType_PUSH_FILE, true
	case storage.TaskTypeAgentUpdate:
		return api.TaskType_AGENT_UPDATE, true
	default:
		return api.TaskType_EXEC_COMMAND, false
	}
}

func (s *Server) GetTaskPackage(in *api.GetTaskPackageRequest, stream api.TaskService_GetTaskPackageServer) error {
	ctx := stream.Context()
	s.logger.Info("Received GetTaskPackage request", "task_id", in.GetTaskId())
	return s.getTaskPackage(in, stream, ctx)
}

func (s *Server) getTaskPackage(in *api.GetTaskPackageRequest, stream api.TaskService_GetTaskPackageServer, ctx context.Context) error {
	taskID, err := uuid.Parse(in.GetTaskId())
	if err != nil {
		s.logger.Error("Failed to parse task ID for package request", "task_id", in.GetTaskId(), "error", err)
		return status.Errorf(codes.InvalidArgument, "Invalid task ID format")
	}

	// 1. Получаем детали задачи из БД
	task, err := s.storage.Task.GetTaskByID(ctx, taskID)
	if err != nil {
		s.logger.Error("Failed to get task details for package", "task_id", taskID, "error", err)
		return status.Errorf(codes.NotFound, "Task not found")
	}

	payloadReader, payloadSizeBytes, err := s.buildTaskPayload(task)
	if err != nil {
		return err
	}

	const payloadChunkSize = 64 * 1024
	for {
		chunk := make([]byte, payloadChunkSize)
		n, err := payloadReader.Read(chunk)
		if err == io.EOF {
			break
		}
		if err != nil {
			s.logger.Error("Failed to read chunk from payload buffer", "task_id", taskID, "error", err)
			return status.Errorf(codes.Internal, "Failed to stream task payload")
		}

		if err := stream.Send(&api.FileChunk{Content: chunk[:n]}); err != nil {
			s.logger.Error("Failed to send chunk to agent", "task_id", taskID, "error", err)
			return status.Errorf(codes.Internal, "Failed to stream payload to agent")
		}
	}

	s.logger.Info("Successfully streamed package to agent", "task_id", taskID, "size_bytes", payloadSizeBytes)
	return nil

	// 2. Проверяем, есть ли файлы для пакета
	if len(task.PackageFiles) == 0 {
		s.logger.Warn("Task has no package files", "task_id", taskID)
		return status.Errorf(codes.NotFound, "Task has no package files associated with it")
	}

	s.logger.Info("Found package files for task", "task_id", taskID, "file_count", len(task.PackageFiles))

	// 3. Создаем ZIP-архив в памяти
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	for _, filePath := range task.PackageFiles {
		s.logger.Debug("Adding file to archive", "path", filePath)
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			s.logger.Error("Failed to read package file", "path", filePath, "task_id", taskID, "error", err)
			return status.Errorf(codes.Internal, "Failed to read a package file: %s", filePath)
		}

		f, err := zipWriter.Create(filepath.Base(filePath))
		if err != nil {
			s.logger.Error("Failed to create file in zip archive", "path", filePath, "task_id", taskID, "error", err)
			return status.Errorf(codes.Internal, "Failed to create file in zip")
		}
		_, err = f.Write(fileBytes)
		if err != nil {
			s.logger.Error("Failed to write file to zip archive", "path", filePath, "task_id", taskID, "error", err)
			return status.Errorf(codes.Internal, "Failed to write file to zip")
		}
	}

	if err := zipWriter.Close(); err != nil {
		s.logger.Error("Failed to close zip writer", "task_id", taskID, "error", err)
		return status.Errorf(codes.Internal, "Failed to finalize package archive")
	}

	// 4. Отправляем архив потоком
	const chunkSize = 64 * 1024 // 64 KB
	reader := bytes.NewReader(buf.Bytes())

	for {
		chunk := make([]byte, chunkSize)
		n, err := reader.Read(chunk)
		if err == io.EOF {
			break
		}
		if err != nil {
			s.logger.Error("Failed to read chunk from archive buffer", "task_id", taskID, "error", err)
			return status.Errorf(codes.Internal, "Failed to stream package")
		}

		if err := stream.Send(&api.FileChunk{Content: chunk[:n]}); err != nil {
			s.logger.Error("Failed to send chunk to agent", "task_id", taskID, "error", err)
			return status.Errorf(codes.Internal, "Failed to stream package to agent")
		}
	}

	s.logger.Info("Successfully streamed package to agent", "task_id", taskID, "size_bytes", buf.Len())
	return nil
}

func (s *Server) buildTaskPayload(task *storage.Task) (*bytes.Reader, int, error) {
	if len(task.PackageFiles) == 0 {
		s.logger.Warn("Task has no package files", "task_id", task.ID)
		return nil, 0, status.Errorf(codes.NotFound, "Task has no package files associated with it")
	}

	s.logger.Info("Found package files for task", "task_id", task.ID, "file_count", len(task.PackageFiles))

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	for _, filePath := range task.PackageFiles {
		s.logger.Debug("Adding file to archive", "path", filePath)
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			s.logger.Error("Failed to read package file", "path", filePath, "task_id", task.ID, "error", err)
			return nil, 0, status.Errorf(codes.Internal, "Failed to read a package file: %s", filePath)
		}

		f, err := zipWriter.Create(filepath.Base(filePath))
		if err != nil {
			s.logger.Error("Failed to create file in zip archive", "path", filePath, "task_id", task.ID, "error", err)
			return nil, 0, status.Errorf(codes.Internal, "Failed to create file in zip")
		}
		if _, err := f.Write(fileBytes); err != nil {
			s.logger.Error("Failed to write file to zip archive", "path", filePath, "task_id", task.ID, "error", err)
			return nil, 0, status.Errorf(codes.Internal, "Failed to write file to zip")
		}
	}

	if err := zipWriter.Close(); err != nil {
		s.logger.Error("Failed to close zip writer", "task_id", task.ID, "error", err)
		return nil, 0, status.Errorf(codes.Internal, "Failed to finalize package archive")
	}

	return bytes.NewReader(buf.Bytes()), buf.Len(), nil
}

func (s *Server) AssignTasks(stream api.TaskService_AssignTasksServer) error {
	s.logger.Info("New agent connected for task assignment.")
	ctx := stream.Context()

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Errorf(codes.DataLoss, "AssignTasks: failed to get metadata")
	}
	agentIDValues := md.Get("agent-id")
	if len(agentIDValues) == 0 {
		return status.Errorf(codes.InvalidArgument, "AssignTasks: 'agent-id' metadata is missing")
	}
	agentID, err := uuid.Parse(agentIDValues[0])
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "AssignTasks: invalid 'agent-id' format")
	}
	s.logger.Info("Agent identified", "agent_id", agentID)

	ackChan := make(chan *api.TaskAcknowledgement)
	errChan := make(chan error, 1)
	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				errChan <- err
				return
			}
			ackChan <- in
		}
	}()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Agent stream closed by context", "agent_id", agentID, "error", ctx.Err())
			return ctx.Err()
		case err := <-errChan:
			if err == io.EOF {
				s.logger.Info("Agent disconnected cleanly", "agent_id", agentID)
				return nil
			}
			s.logger.Error("Error receiving from agent stream", "agent_id", agentID, "error", err)
			return err
		case ack := <-ackChan:
			s.logger.Info("Received acknowledgement", "agent_id", agentID, "task_id", ack.TaskId, "status", ack.Status)
			s.handleTaskAcknowledgement(ctx, ack)
		case <-ticker.C:
			s.dispatchQueuedTasks(ctx, stream, agentID)

			tasks, err := s.storage.Task.GetPendingTasksByAgent(ctx, agentID)
			if err != nil {
				s.logger.Error("Failed to get pending tasks", "agent_id", agentID, "error", err)
				continue
			}
			if len(tasks) == 0 {
				continue
			}
			s.logger.Info("Found pending tasks for agent", "count", len(tasks), "agent_id", agentID)
			for _, task := range tasks {
				if err := s.sendTaskToAgent(ctx, stream, agentID, task); err != nil {
					return err
				}
			}
		}
	}
}

func (s *Server) handleTaskAcknowledgement(ctx context.Context, ack *api.TaskAcknowledgement) {
	if ack == nil || ack.GetStatus() != api.TaskAcknowledgementStatus_PROCESSING {
		return
	}

	taskID, err := uuid.Parse(ack.GetTaskId())
	if err != nil {
		s.logger.Warn("Received acknowledgement with invalid task ID", "task_id", ack.GetTaskId(), "status", ack.GetStatus(), "error", err)
		return
	}

	updated, err := s.storage.Task.MarkTaskStartedIfCurrent(ctx, taskID, storage.TaskStatusAssigned, time.Now().UTC())
	if err != nil {
		s.logger.Error("Failed to mark task as running", "task_id", taskID, "error", err)
		return
	}
	if !updated {
		s.logger.Info("Skipped running status update because task state already changed", "task_id", taskID)
		return
	}

	updatedTask, err := s.storage.Task.GetTaskByID(ctx, taskID)
	if err != nil {
		s.logger.Error("Failed to get running task for event publishing", "task_id", taskID, "error", err)
		return
	}
	s.broker.Broadcast(updatedTask)
}

func (s *Server) dispatchQueuedTasks(ctx context.Context, stream api.TaskService_AssignTasksServer, agentID uuid.UUID) {
	if s.taskQueue == nil {
		return
	}

	queueDepth := len(s.taskQueue)
	if queueDepth == 0 {
		return
	}

	deferred := make([]uuid.UUID, 0)
	for i := 0; i < queueDepth; i++ {
		taskID := <-s.taskQueue
		task, err := s.storage.Task.GetTaskByID(ctx, taskID)
		if err != nil {
			s.logger.Error("Failed to load queued task", "task_id", taskID, "error", err)
			continue
		}
		if task.AgentID != agentID {
			deferred = append(deferred, taskID)
			continue
		}
		if task.Status != storage.TaskStatusPending {
			s.logger.Info("Skipping queued task because it is no longer pending", "task_id", task.ID, "status", task.Status)
			continue
		}
		if err := s.sendTaskToAgent(ctx, stream, agentID, task); err != nil {
			s.logger.Error("Failed to dispatch queued task to agent", "task_id", task.ID, "agent_id", agentID, "error", err)
			deferred = append(deferred, taskID)
		}
	}

	for _, taskID := range deferred {
		s.taskQueue <- taskID
	}
}

func (s *Server) sendTaskToAgent(ctx context.Context, stream api.TaskService_AssignTasksServer, agentID uuid.UUID, task *storage.Task) error {
	taskType, ok := mapStorageTaskType(task.TaskType)
	if !ok {
		s.logger.Error("Unsupported task type in storage", "task_id", task.ID, "agent_id", agentID, "task_type", task.TaskType)
		return nil
	}

	taskCmd := &api.TaskCommand{
		TaskId:           task.ID.String(),
		TaskType:         taskType,
		Command:          task.Command.String,
		Args:             task.Args,
		EntrypointScript: task.EntrypointScript.String,
		SourcePath:       task.SourcePath.String,
		DestinationPath:  task.DestinationPath.String,
		TimeoutSeconds:   task.TimeoutSeconds.Int32,
	}
	if err := stream.Send(taskCmd); err != nil {
		s.logger.Error("Failed to send task to agent", "task_id", task.ID, "agent_id", agentID, "error", err)
		return err
	}
	updated, err := s.storage.Task.UpdateTaskStatusIfCurrent(ctx, task.ID, storage.TaskStatusPending, storage.TaskStatusAssigned)
	if err != nil {
		s.logger.Error("Failed to update task status to assigned", "task_id", task.ID, "error", err)
	} else if updated {
		updatedTask, err := s.storage.Task.GetTaskByID(ctx, task.ID)
		if err != nil {
			s.logger.Error("Failed to get updated task for event publishing in AssignTasks", "task_id", task.ID, "error", err)
		} else {
			s.broker.Broadcast(updatedTask)
		}
	} else {
		s.logger.Info("Skipped assigned status update because task state already changed", "task_id", task.ID)
	}
	s.logger.Info("Sent task to agent", "task_id", task.ID, "agent_id", agentID)
	return nil
}

func StartServer(cfg config.ServerConfig, logger *slog.Logger, storage *storage.Storage, notifier *notifier.MultiNotifier, taskQueue chan uuid.UUID, broker *events.EventBroker, ruleEngine *NotificationRuleEngine, router *NotificationRouter, dispatcher *NotificationDeliveryDispatcher) {
	lis, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		logger.Error("failed to listen", "address", cfg.ListenAddress, "error", err)
		os.Exit(1)
	}
	s := grpc.NewServer()
	server := NewServer(logger, storage, notifier, taskQueue, broker)
	server.SetNotificationPipeline(ruleEngine, router)
	server.SetNotificationDeliveryDispatcher(dispatcher)
	api.RegisterAgentServiceServer(s, server)
	api.RegisterTaskServiceServer(s, server)
	logger.Info("server listening", "address", lis.Addr())
	if err := s.Serve(lis); err != nil {
		logger.Error("failed to serve", "error", err)
		os.Exit(1)
	}
}
