package internal

import (
	"context"
	"database/sql"
	"io"
	"os"
	"testing"
	"time"

	"agent-management/pkg/api"
	"agent-management/server/internal/storage"
	//"agent-management/server/internal/storage/mocks"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/metadata"
)

// Mock for the AgentService_StreamLogsServer
type mockStreamLogsServer struct {
	api.AgentService_StreamLogsServer
	recv chan *api.LogEntry
	sent chan *api.StreamLogsResponse
	err  chan error
	ctx  context.Context
}

func (m *mockStreamLogsServer) Context() context.Context {
	return m.ctx
}

func (m *mockStreamLogsServer) Recv() (*api.LogEntry, error) {
	select {
	case entry, ok := <-m.recv:
		if !ok {
			return nil, io.EOF
		}
		return entry, nil
	case err := <-m.err:
		return nil, err
	}
}

func (m *mockStreamLogsServer) SendAndClose(res *api.StreamLogsResponse) error {
	m.sent <- res
	return nil
}

func TestStreamLogs(t *testing.T) {
	s, _, _, mockLogRepo, _, _, _ := setupTestServer(t)
	agentID := uuid.New()
	taskID := uuid.New()

	mockStream := &mockStreamLogsServer{
		recv: make(chan *api.LogEntry, 2),
		sent: make(chan *api.StreamLogsResponse, 1),
		err:  make(chan error, 1),
		ctx:  context.Background(),
	}

	// Entries to be sent by the mock client
	log1 := &api.LogEntry{AgentId: agentID.String(), TaskId: taskID.String(), Timestamp: time.Now().Unix(), Level: "INFO", Message: "Log 1"}
	log2 := &api.LogEntry{AgentId: agentID.String(), TaskId: taskID.String(), Timestamp: time.Now().Unix(), Level: "ERROR", Message: "Log 2"}

	// Expect the CreateLogEntries method to be called once with a slice containing our two logs.
	mockLogRepo.EXPECT().
		CreateLogEntries(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, logs []*storage.Log) error {
			if len(logs) != 2 {
				t.Errorf("expected 2 log entries, got %d", len(logs))
			}
			if logs[0].Message != "Log 1" || logs[1].Message != "Log 2" {
				t.Error("log messages do not match expected")
			}
			return nil
		}).
		Times(1)

	// Run the server method in a goroutine
	go func() {
		err := s.StreamLogs(mockStream)
		if err != nil {
			// This will fail the test if the server returns an unexpected error
			t.Errorf("StreamLogs returned an unexpected error: %v", err)
		}
	}()

	// Simulate the client sending logs
	mockStream.recv <- log1
	mockStream.recv <- log2
	close(mockStream.recv) // This will cause Recv() to return io.EOF

	// Wait for the server to send its response
	select {
	case res := <-mockStream.sent:
		if !res.Success {
			t.Error("expected success=true in StreamLogsResponse")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server response")
	}
}

// Mock for the TaskService_AssignTasksServer
type mockAssignTasksServer struct {
	api.TaskService_AssignTasksServer
	sent    chan *api.TaskCommand
	recv    chan *api.TaskAcknowledgement
	err     chan error
	grpcCtx context.Context
}

func (m *mockAssignTasksServer) Context() context.Context {
	return m.grpcCtx
}

func (m *mockAssignTasksServer) Send(cmd *api.TaskCommand) error {
	m.sent <- cmd
	return nil
}

func (m *mockAssignTasksServer) Recv() (*api.TaskAcknowledgement, error) {
	select {
	case ack, ok := <-m.recv:
		if !ok {
			return nil, io.EOF
		}
		return ack, nil
	case err := <-m.err:
		return nil, err
	}
}

type mockGetTaskPackageServer struct {
	api.TaskService_GetTaskPackageServer
	ctx    context.Context
	chunks [][]byte
}

func (m *mockGetTaskPackageServer) Context() context.Context {
	return m.ctx
}

func (m *mockGetTaskPackageServer) Send(chunk *api.FileChunk) error {
	m.chunks = append(m.chunks, append([]byte(nil), chunk.GetContent()...))
	return nil
}

type mockUploadTaskFileServer struct {
	api.AgentService_UploadTaskFileServer
	ctx  context.Context
	recv chan *api.UploadTaskFileRequest
	sent chan *api.UploadTaskFileResponse
	err  chan error
}

func (m *mockUploadTaskFileServer) Context() context.Context {
	return m.ctx
}

func (m *mockUploadTaskFileServer) Recv() (*api.UploadTaskFileRequest, error) {
	select {
	case req, ok := <-m.recv:
		if !ok {
			return nil, io.EOF
		}
		return req, nil
	case err := <-m.err:
		return nil, err
	}
}

func (m *mockUploadTaskFileServer) SendAndClose(res *api.UploadTaskFileResponse) error {
	m.sent <- res
	return nil
}

func TestAssignTasks(t *testing.T) {
	s, _, mockTaskRepo, _, _, _, _ := setupTestServer(t)
	agentID := uuid.New()
	taskID := uuid.New()

	// 1. Setup mock stream
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	md := metadata.New(map[string]string{"agent-id": agentID.String()})
	grpcCtx := metadata.NewIncomingContext(ctx, md)

	mockStream := &mockAssignTasksServer{
		sent:    make(chan *api.TaskCommand, 1),
		recv:    make(chan *api.TaskAcknowledgement),
		err:     make(chan error, 1),
		grpcCtx: grpcCtx,
	}

	// 2. Setup mock repository expectations
	taskToSend := &storage.Task{
		ID:       taskID,
		AgentID:  agentID,
		TaskType: storage.TaskTypeExecCommand,
		Command:  sql.NullString{String: "echo", Valid: true},
		Args:     []string{"hello"},
		Status:   storage.TaskStatusPending,
	}

	// Expect GetPendingTasks to be called periodically. We'll only check the first time.
	mockTaskRepo.EXPECT().
		GetPendingTasksByAgent(gomock.Any(), agentID).
		Return([]*storage.Task{taskToSend}, nil).
		MinTimes(1)

	// Expect UpdateTaskStatus to be called after the task is sent.
	gomock.InOrder(
		mockTaskRepo.EXPECT().
			UpdateTaskStatusIfCurrent(gomock.Any(), taskID, storage.TaskStatusPending, storage.TaskStatusAssigned).
			Return(true, nil),
		mockTaskRepo.EXPECT().
			GetTaskByID(gomock.Any(), taskID).
			Return(taskToSend, nil), // For the broadcast
	)

	// 3. Run AssignTasks in a goroutine
	go func() {
		// Override the ticker in the server to a shorter duration for the test
		// This is a bit of a hack. A better way would be to inject the ticker.
		// For this test, we'll rely on the default 15s ticker and just wait.
		s.AssignTasks(mockStream)
	}()

	// 4. Verify the task is sent
	select {
	case sentCmd := <-mockStream.sent:
		if sentCmd.TaskId != taskID.String() {
			t.Errorf("Expected task ID %s, but got %s", taskID.String(), sentCmd.TaskId)
		}
		if sentCmd.Command != "echo" {
			t.Errorf("Expected command 'echo', but got '%s'", sentCmd.Command)
		}
	case <-time.After(16 * time.Second): // Wait for slightly longer than the ticker
		t.Fatal("timed out waiting for server to send task")
	}
}

func TestAssignTasksSkipsAssignedOverwriteWhenTaskAlreadyFinished(t *testing.T) {
	s, _, mockTaskRepo, _, _, _, _ := setupTestServer(t)
	agentID := uuid.New()
	taskID := uuid.New()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	md := metadata.New(map[string]string{"agent-id": agentID.String()})
	grpcCtx := metadata.NewIncomingContext(ctx, md)

	mockStream := &mockAssignTasksServer{
		sent:    make(chan *api.TaskCommand, 1),
		recv:    make(chan *api.TaskAcknowledgement),
		err:     make(chan error, 1),
		grpcCtx: grpcCtx,
	}

	taskToSend := &storage.Task{
		ID:       taskID,
		AgentID:  agentID,
		TaskType: storage.TaskTypePushFile,
		Status:   storage.TaskStatusPending,
	}

	mockTaskRepo.EXPECT().
		GetPendingTasksByAgent(gomock.Any(), agentID).
		Return([]*storage.Task{taskToSend}, nil).
		MinTimes(1)

	mockTaskRepo.EXPECT().
		UpdateTaskStatusIfCurrent(gomock.Any(), taskID, storage.TaskStatusPending, storage.TaskStatusAssigned).
		Return(false, nil)

	mockTaskRepo.EXPECT().
		GetTaskByID(gomock.Any(), taskID).
		Times(0)

	go func() {
		s.AssignTasks(mockStream)
	}()

	select {
	case sentCmd := <-mockStream.sent:
		if sentCmd.TaskId != taskID.String() {
			t.Errorf("Expected task ID %s, but got %s", taskID.String(), sentCmd.TaskId)
		}
	case <-time.After(16 * time.Second):
		t.Fatal("timed out waiting for server to send task")
	}
}

func TestGetTaskPackagePushFile(t *testing.T) {
	s, _, mockTaskRepo, _, _, _, _ := setupTestServer(t)
	taskID := uuid.New()

	tmpFile, err := os.CreateTemp(t.TempDir(), "fetch-file-*")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	content := []byte("fetch-file-content")
	if _, err := tmpFile.Write(content); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	mockTaskRepo.EXPECT().
		GetTaskByID(gomock.Any(), taskID).
		Return(&storage.Task{
			ID:           taskID,
			TaskType:     storage.TaskTypePushFile,
			PackageFiles: []string{tmpFile.Name()},
		}, nil).
		Times(1)

	stream := &mockGetTaskPackageServer{ctx: context.Background()}
	req := &api.GetTaskPackageRequest{TaskId: taskID.String()}

	if err := s.GetTaskPackage(req, stream); err != nil {
		t.Fatalf("GetTaskPackage failed: %v", err)
	}

	var received []byte
	for _, chunk := range stream.chunks {
		received = append(received, chunk...)
	}

	if len(received) == 0 {
		t.Fatal("expected non-empty streamed archive")
	}
}

func TestUploadTaskFileFetchFile(t *testing.T) {
	s, _, mockTaskRepo, _, _, _, _ := setupTestServer(t)
	agentID := uuid.New()
	taskID := uuid.New()
	destinationDir := t.TempDir()
	destinationPath := destinationDir + "\\received.txt"
	content := []byte("content-from-agent")

	mockTaskRepo.EXPECT().
		GetTaskByID(gomock.Any(), taskID).
		Return(&storage.Task{
			ID:              taskID,
			AgentID:         agentID,
			TaskType:        storage.TaskTypeFetchFile,
			DestinationPath: sql.NullString{String: destinationPath, Valid: true},
		}, nil).
		Times(1)

	stream := &mockUploadTaskFileServer{
		ctx:  context.Background(),
		recv: make(chan *api.UploadTaskFileRequest, 2),
		sent: make(chan *api.UploadTaskFileResponse, 1),
		err:  make(chan error, 1),
	}

	stream.recv <- &api.UploadTaskFileRequest{AgentId: agentID.String(), TaskId: taskID.String(), Content: content}
	close(stream.recv)

	if err := s.UploadTaskFile(stream); err != nil {
		t.Fatalf("UploadTaskFile failed: %v", err)
	}

	select {
	case res := <-stream.sent:
		if !res.Success {
			t.Fatal("expected success=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upload response")
	}

	received, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(received) != string(content) {
		t.Fatalf("expected %q, got %q", string(content), string(received))
	}
}
