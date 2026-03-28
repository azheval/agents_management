package internal

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	pb "agent-management/pkg/api"

	"github.com/google/uuid"
	"github.com/inconshreveable/go-update"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	//"google.golang.org/grpc/status"
)

const agentIDFileName = "agent.id"

func getOrCreateAgentID(logger *slog.Logger) (string, error) {
	// Try to read the agent ID from the file
	data, err := os.ReadFile(agentIDFileName)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			logger.Info("Found existing agent ID", "agent_id", id, "file", agentIDFileName)
			return id, nil
		}
	}

	if !os.IsNotExist(err) {
		// Any error other than "not found" is a problem
		logger.Error("Failed to read agent ID file", "file", agentIDFileName, "error", err)
		return "", err
	}

	// File does not exist or is empty, so create a new ID
	logger.Info("No existing agent ID found, generating a new one.")
	newID := uuid.New().String()

	// Save the new ID to the file
	err = os.WriteFile(agentIDFileName, []byte(newID), 0644)
	if err != nil {
		logger.Error("Failed to write new agent ID to file", "file", agentIDFileName, "error", err)
		return "", err
	}

	logger.Info("Saved new agent ID", "agent_id", newID, "file", agentIDFileName)
	return newID, nil
}

func getIPAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return []string{}
	}
	var ipAddrs []string
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ipAddrs = append(ipAddrs, ipnet.IP.String())
			}
		}
	}
	return ipAddrs
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}

	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ip := ipnet.IP
				if ip.IsPrivate() {
					return ip.String()
				}
			}
		}
	}
	return ""
}

func StartAgent(serverAddr string) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Get or create a persistent UUID for the agent
	agentID, err := getOrCreateAgentID(logger)
	if err != nil {
		logger.Error("Fatal: could not get or create agent ID", "error", err)
		os.Exit(1)
	}
	logger.Info("Starting agent with ID", "agent_id", agentID)

	// Set up a connection to the server.
	conn, err := grpc.NewClient(serverAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("did not connect", "error", err)
		os.Exit(1)
	}
	defer conn.Close()
	agentClient := pb.NewAgentServiceClient(conn)

	// Contact the server and print out its response.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hostname, err := os.Hostname()
	if err != nil {
		logger.Error("could not get hostname", "error", err)
		hostname = "unknown"
	}

	ip := getLocalIP()

	r, err := agentClient.RegisterAgent(ctx, &pb.RegisterAgentRequest{
		AgentId:      agentID,
		Hostname:     hostname,
		Os:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		AgentVersion: "0.0.1",
		IpAddresses:  []string{ip},
	})
	if err != nil {
		logger.Error("could not register agent", "error", err)
		os.Exit(1)
	}
	logger.Info("Agent registration", "message", r.GetMessage())

	// Start Heartbeat
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				logger.Info("Gathering metrics for heartbeat...")
				metrics, err := GetSystemMetrics(logger) // err is currently always nil from GetSystemMetrics

				// Prepare heartbeat request
				req := &pb.HeartbeatRequest{
					AgentId:   agentID,
					Timestamp: time.Now().Unix(),
				}

				if err != nil {
					logger.Error("GetSystemMetrics returned an error", "error", err)
				} else if metrics != nil {
					req.CpuUsage = float32(metrics.CPUUsagePercent)
					req.RamUsage = float32(metrics.RAMUsagePercent)
					if len(metrics.DiskUsage) > 0 {
						req.DiskUsage = make(map[string]float32)
						for k, v := range metrics.DiskUsage {
							req.DiskUsage[k] = float32(v)
						}
					}
					logger.Info("Metrics gathered", "cpu", req.CpuUsage, "ram", req.RamUsage)
				}

				_, err = agentClient.SendHeartbeat(context.Background(), req)
				if err != nil {
					logger.Error("could not send heartbeat with metrics", "error", err)
				} else {
					logger.Info("Heartbeat sent.")
				}
			}
		}
	}()

	logger.Info("Starting task listener...")
	// Run the task listener in a loop to handle reconnects
	for {
		err := listenForTasks(agentID, conn, logger)
		if err != nil {
			logger.Error("Task listener failed, will reconnect in 15 seconds", "error", err)
			time.Sleep(15 * time.Second)
		}
	}
}

func listenForTasks(agentID string, conn *grpc.ClientConn, logger *slog.Logger) error {
	taskClient := pb.NewTaskServiceClient(conn)
	agentClient := pb.NewAgentServiceClient(conn) // For submitting results/logs

	// Create a context with the agent-id in metadata
	md := metadata.New(map[string]string{"agent-id": agentID})
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	stream, err := taskClient.AssignTasks(ctx)
	if err != nil {
		return err
	}
	logger.Info("Connected to task assignment stream")

	// Goroutine to send acknowledgements back to the server
	ackChan := make(chan *pb.TaskAcknowledgement)
	go func() {
		for ack := range ackChan {
			if err := stream.Send(ack); err != nil {
				logger.Error("Failed to send task acknowledgement", "task_id", ack.TaskId, "error", err)
			}
		}
	}()
	defer close(ackChan)

	// Main loop to receive tasks
	for {
		task, err := stream.Recv()
		if err == io.EOF {
			logger.Info("Task stream ended by server.")
			return nil
		}
		if err != nil {
			return err
		}

		logger.Info("Received task", "task_id", task.GetTaskId(), "type", task.GetTaskType())
		ackChan <- &pb.TaskAcknowledgement{
			TaskId: task.GetTaskId(),
			Status: pb.TaskAcknowledgementStatus_RECEIVED,
		}

		// Execute the task in a new goroutine to avoid blocking the stream
		go handleTask(agentID, taskClient, agentClient, task, logger)
	}
}

func handleTask(agentID string, taskClient pb.TaskServiceClient, agentClient pb.AgentServiceClient, task *pb.TaskCommand, logger *slog.Logger) {
	taskLogger := logger.With("task_id", task.GetTaskId(), "type", task.GetTaskType().String())
	taskLogger.Info("Handling task")

	switch task.GetTaskType() {
	case pb.TaskType_EXEC_COMMAND:
		handleExecCommandTask(agentID, agentClient, task, taskLogger)
	case pb.TaskType_EXEC_PYTHON_SCRIPT:
		handlePythonScriptTask(agentID, taskClient, agentClient, task, taskLogger)
	case pb.TaskType_FETCH_FILE:
		handleFetchFileTask(agentID, agentClient, task, taskLogger)
	case pb.TaskType_PUSH_FILE:
		handlePushFileTask(agentID, taskClient, agentClient, task, taskLogger)
	case pb.TaskType_AGENT_UPDATE:
		handleAgentUpdateTask(agentID, taskClient, agentClient, task, taskLogger)
	default:
		taskLogger.Warn("Unsupported task type")
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, time.Now(), "Unsupported task type")
	}
}

func handleAgentUpdateTask(agentID string, taskClient pb.TaskServiceClient, agentClient pb.AgentServiceClient, task *pb.TaskCommand, taskLogger *slog.Logger) {
	startTime := time.Now()
	taskLogger.Info("Executing agent update task", "checksum", task.GetCommand(), "source_path", task.GetSourcePath())

	checksumHex := strings.TrimSpace(task.GetCommand())
	if checksumHex == "" {
		args := task.GetArgs()
		if len(args) > 0 {
			checksumHex = strings.TrimSpace(args[0])
		}
	}
	if checksumHex == "" {
		taskLogger.Error("Checksum info is missing from task")
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Checksum info is missing")
		return
	}

	req := &pb.GetTaskPackageRequest{TaskId: task.GetTaskId()}
	packageStream, err := taskClient.GetTaskPackage(context.Background(), req)
	if err != nil {
		taskLogger.Error("Failed to initiate update package download", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to start update package download")
		return
	}

	zipBuffer := new(bytes.Buffer)
	for {
		chunk, err := packageStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			taskLogger.Error("Failed to download update package chunk", "error", err)
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed during update package download")
			return
		}
		zipBuffer.Write(chunk.GetContent())
	}

	zipReader, err := zip.NewReader(bytes.NewReader(zipBuffer.Bytes()), int64(zipBuffer.Len()))
	if err != nil {
		taskLogger.Error("Failed to read update package archive", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to read update package archive")
		return
	}
	if len(zipReader.File) == 0 {
		taskLogger.Error("Update package archive is empty")
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Update package archive is empty")
		return
	}

	rc, err := zipReader.File[0].Open()
	if err != nil {
		taskLogger.Error("Failed to open update binary inside archive", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to open update binary inside archive")
		return
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		msg := fmt.Sprintf("Failed to read downloaded content: %v", err)
		taskLogger.Error(msg)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, msg)
		return
	}
	taskLogger.Info("Update package downloaded. Verifying checksum...")

	// 3. Verify the checksum.
	hasher := sha256.New()
	hasher.Write(body)
	calculatedChecksum := hex.EncodeToString(hasher.Sum(nil))

	if calculatedChecksum != checksumHex {
		msg := fmt.Sprintf("Checksum verification failed. Expected: %s, Got: %s", checksumHex, calculatedChecksum)
		taskLogger.Error(msg)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, msg)
		return
	}
	taskLogger.Info("Checksum verified. Applying update...")

	// 4. Atomically replace the current executable using go-update.
	err = update.Apply(bytes.NewReader(body), update.Options{})
	if err != nil {
		// The library might have created a backup file. Attempt to roll back.
		if rerr := update.RollbackError(err); rerr != nil {
			taskLogger.Error("Failed to rollback update after error", "rollback_error", rerr)
		}
		msg := fmt.Sprintf("Failed to apply update: %v", err)
		taskLogger.Error(msg)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, msg)
		return
	}

	taskLogger.Info("Update applied successfully. Agent will restart.")

	// 5. Submit success and then exit. The service manager should restart the agent.
	submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_SUCCESS, 0, startTime, "Agent update applied. Restarting.")
	taskLogger.Info("Finished agent update task. Exiting to complete the update.")
	os.Exit(0)
}

func handleExecCommandTask(agentID string, agentClient pb.AgentServiceClient, task *pb.TaskCommand, taskLogger *slog.Logger) {
	startTime := time.Now()
	taskLogger.Info("Executing command task")

	// 1. Set up context with timeout
	timeout := time.Duration(task.GetTimeoutSeconds()) * time.Second
	if task.GetTimeoutSeconds() <= 0 {
		timeout = 30 * time.Minute // Default timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 2. Open a log stream to the server
	logStream, err := agentClient.StreamLogs(context.Background()) // Use a background context for the log stream
	if err != nil {
		taskLogger.Error("Failed to open log stream", "error", err)
		// We still need to report the task failure
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to open log stream")
		return
	}

	// 3. Use exec.CommandContext to run the command
	cmd := exec.CommandContext(ctx, task.GetCommand(), task.GetArgs()...)

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	// 4. Pipe stdout/stderr to the log stream
	var wg sync.WaitGroup
	wg.Add(2)
	go pipeLogs(stdoutPipe, logStream, agentID, task.GetTaskId(), "INFO", &wg, taskLogger, nil)
	go pipeLogs(stderrPipe, logStream, agentID, task.GetTaskId(), "ERROR", &wg, taskLogger, nil)

	// 5. Wait for command to finish
	err = cmd.Start()
	if err != nil {
		taskLogger.Error("Failed to start command", "error", err)
		wg.Wait() // Wait for loggers to finish
		logStream.CloseAndRecv()
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, err.Error())
		return
	}

	wg.Wait() // Wait for log streaming to finish
	err = cmd.Wait()

	_, errLogStream := logStream.CloseAndRecv()
	if errLogStream != nil {
		taskLogger.Error("Error closing log stream", "error", errLogStream)
	}

	// 6. Submit the final result
	exitCode := cmd.ProcessState.ExitCode()
	finalStatus := pb.TaskStatus_SUCCESS
	errMsg := ""

	if err != nil {
		errMsg = err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			finalStatus = pb.TaskStatus_TIMED_OUT
			taskLogger.Warn("Task timed out")
		} else {
			finalStatus = pb.TaskStatus_FAILED
			taskLogger.Error("Task execution failed", "error", err)
		}
	}

	submitResult(context.Background(), agentClient, agentID, task, finalStatus, exitCode, startTime, errMsg)
	taskLogger.Info("Finished task", "status", finalStatus, "exit_code", exitCode)
}

func pipeLogs(pipe io.ReadCloser, stream pb.AgentService_StreamLogsClient, agentID, taskID, level string, wg *sync.WaitGroup, logger *slog.Logger, capture *bytes.Buffer) {
	defer wg.Done()
	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := decodeProcessOutputLine(scanner.Bytes())
		if capture != nil {
			capture.WriteString(line)
			capture.WriteByte('\n')
		}
		logEntry := &pb.LogEntry{
			AgentId:   agentID,
			TaskId:    taskID,
			Timestamp: time.Now().Unix(),
			Level:     level,
			Message:   line,
		}
		if err := stream.Send(logEntry); err != nil {
			logger.Error("Failed to send log entry", "error", err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		logger.Error("Error reading from command pipe", "error", err)
	}
}

func submitResult(ctx context.Context, client pb.AgentServiceClient, agentID string, task *pb.TaskCommand, status pb.TaskStatus, exitCode int, startTime time.Time, output string) {
	duration := time.Since(startTime)
	req := &pb.SubmitTaskResultRequest{
		AgentId:    agentID,
		TaskId:     task.GetTaskId(),
		Status:     status,
		ExitCode:   int32(exitCode),
		Output:     output,
		DurationMs: duration.Milliseconds(),
	}
	_, err := client.SubmitTaskResult(ctx, req)
	if err != nil {
		slog.With("task_id", task.GetTaskId()).Error("Failed to submit final task result", "error", err)
	}
}
