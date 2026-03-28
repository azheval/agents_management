package internal

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "agent-management/pkg/api"
)

func handlePythonScriptTask(agentID string, taskClient pb.TaskServiceClient, agentClient pb.AgentServiceClient, task *pb.TaskCommand, taskLogger *slog.Logger) {
	startTime := time.Now()
	taskLogger.Info("Executing Python script task")

	// 1. Создаем временный каталог для задачи
	taskDir, err := os.MkdirTemp("", "agent-task-"+task.GetTaskId()+"-*")
	if err != nil {
		taskLogger.Error("Failed to create temp directory for task", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to create temp dir")
		return
	}
	defer os.RemoveAll(taskDir) // Очищаем за собой
	taskLogger.Info("Created temp directory", "path", taskDir)

	// 2. Скачиваем пакет
	req := &pb.GetTaskPackageRequest{TaskId: task.GetTaskId()}
	packageStream, err := taskClient.GetTaskPackage(context.Background(), req)
	if err != nil {
		taskLogger.Error("Failed to initiate package download", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to start package download")
		return
	}

	zipBuffer := new(bytes.Buffer)
	for {
		chunk, err := packageStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			taskLogger.Error("Failed to download package chunk", "error", err)
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed during package download")
			return
		}
		zipBuffer.Write(chunk.GetContent())
	}
	taskLogger.Info("Package downloaded successfully", "size_bytes", zipBuffer.Len())

	// 3. Распаковываем архив
	zipReader, err := zip.NewReader(bytes.NewReader(zipBuffer.Bytes()), int64(zipBuffer.Len()))
	if err != nil {
		taskLogger.Error("Failed to read zip archive", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to read zip archive")
		return
	}

	for _, f := range zipReader.File {
		fpath := filepath.Join(taskDir, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			taskLogger.Error("Failed to create dir for file", "path", fpath, "error", err)
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to create dir for file")
			return
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			taskLogger.Error("Failed to create file in temp dir", "path", fpath, "error", err)
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to create file")
			return
		}

		rc, err := f.Open()
		if err != nil {
			taskLogger.Error("Failed to open file in zip", "name", f.Name, "error", err)
			outFile.Close()
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to open file in zip")
			return
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			taskLogger.Error("Failed to extract file", "name", f.Name, "error", err)
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to extract file")
			return
		}
	}
	taskLogger.Info("Package extracted successfully")

	// 4. Запускаем скрипт
	command := "python"
	args := []string{task.GetEntrypointScript()}
	args = append(args, task.GetArgs()...)

	timeout := time.Duration(task.GetTimeoutSeconds()) * time.Second
	if task.GetTimeoutSeconds() <= 0 {
		timeout = 30 * time.Minute // Default timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	logStream, err := agentClient.StreamLogs(context.Background())
	if err != nil {
		taskLogger.Error("Failed to open log stream", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to open log stream")
		return
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = taskDir // Устанавливаем рабочую директорию!

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	var stdoutBuffer bytes.Buffer

	var wg sync.WaitGroup
	wg.Add(2)
	go pipeLogs(stdoutPipe, logStream, agentID, task.GetTaskId(), "INFO", &wg, taskLogger, &stdoutBuffer)
	go pipeLogs(stderrPipe, logStream, agentID, task.GetTaskId(), "ERROR", &wg, taskLogger, nil)

	err = cmd.Start()
	if err != nil {
		taskLogger.Error("Failed to start script", "error", err)
		wg.Wait()
		logStream.CloseAndRecv()
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, err.Error())
		return
	}

	wg.Wait()
	err = cmd.Wait()

	logStream.CloseAndRecv()

	// 5. Отправляем результат
	exitCode := cmd.ProcessState.ExitCode()
	finalStatus := pb.TaskStatus_SUCCESS
	resultOutput := strings.TrimSpace(stdoutBuffer.String())

	if err != nil {
		resultOutput = strings.TrimSpace(err.Error())
		if ctx.Err() == context.DeadlineExceeded {
			finalStatus = pb.TaskStatus_TIMED_OUT
			taskLogger.Warn("Task timed out")
		} else {
			finalStatus = pb.TaskStatus_FAILED
			taskLogger.Error("Task execution failed", "error", err)
		}
	}

	submitResult(context.Background(), agentClient, agentID, task, finalStatus, exitCode, startTime, resultOutput)
	taskLogger.Info("Finished Python script task", "status", finalStatus, "exit_code", exitCode)
}
