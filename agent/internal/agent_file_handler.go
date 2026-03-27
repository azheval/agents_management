package internal

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "agent-management/pkg/api"
)

func handlePushFileTask(agentID string, taskClient pb.TaskServiceClient, agentClient pb.AgentServiceClient, task *pb.TaskCommand, taskLogger *slog.Logger) {
	startTime := time.Now()
	destinationPath := task.GetDestinationPath()
	if destinationPath == "" {
		taskLogger.Error("Destination path is empty")
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Destination path is empty")
		return
	}

	req := &pb.GetTaskPackageRequest{TaskId: task.GetTaskId()}
	fileStream, err := taskClient.GetTaskPackage(context.Background(), req)
	if err != nil {
		taskLogger.Error("Failed to initiate file download", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to start file download")
		return
	}

	zipBuffer := new(bytes.Buffer)
	for {
		chunk, err := fileStream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			taskLogger.Error("Failed to receive file chunk", "error", err)
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed during file download")
			return
		}
		zipBuffer.Write(chunk.GetContent())
	}

	zipReader, err := zip.NewReader(bytes.NewReader(zipBuffer.Bytes()), int64(zipBuffer.Len()))
	if err != nil {
		taskLogger.Error("Failed to read uploaded archive", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to read uploaded archive")
		return
	}
	if len(zipReader.File) == 0 {
		taskLogger.Error("Uploaded archive is empty")
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Uploaded archive is empty")
		return
	}

	written, err := extractPushArchive(zipReader, destinationPath)
	if err != nil {
		taskLogger.Error("Failed to extract pushed file", "path", destinationPath, "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, err.Error())
		return
	}

	taskLogger.Info("PUSH_FILE completed", "path", destinationPath, "size_bytes", written)
	submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_SUCCESS, 0, startTime, "")
}

func handleFetchFileTask(agentID string, agentClient pb.AgentServiceClient, task *pb.TaskCommand, taskLogger *slog.Logger) {
	startTime := time.Now()
	sourcePath := task.GetSourcePath()
	if sourcePath == "" {
		taskLogger.Error("Source path is empty")
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Source path is empty")
		return
	}

	inFile, err := os.Open(sourcePath)
	if err != nil {
		taskLogger.Error("Failed to open source file", "path", sourcePath, "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to open source file")
		return
	}
	defer inFile.Close()

	uploadStream, err := agentClient.UploadTaskFile(context.Background())
	if err != nil {
		taskLogger.Error("Failed to open upload stream", "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to open upload stream")
		return
	}

	buf := make([]byte, 64*1024)
	for {
		n, readErr := inFile.Read(buf)
		if n > 0 {
			sendErr := uploadStream.Send(&pb.UploadTaskFileRequest{
				AgentId: agentID,
				TaskId:  task.GetTaskId(),
				Content: append([]byte(nil), buf[:n]...),
			})
			if sendErr != nil {
				taskLogger.Error("Failed to send file chunk", "path", sourcePath, "error", sendErr)
				submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to upload file")
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			taskLogger.Error("Failed to read source file", "path", sourcePath, "error", readErr)
			submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to read source file")
			return
		}
	}

	if _, err := uploadStream.CloseAndRecv(); err != nil {
		taskLogger.Error("Failed to finalize uploaded file", "path", sourcePath, "error", err)
		submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_FAILED, -1, startTime, "Failed to finalize uploaded file")
		return
	}

	taskLogger.Info("FETCH_FILE completed", "path", sourcePath, "destination", task.GetDestinationPath())
	submitResult(context.Background(), agentClient, agentID, task, pb.TaskStatus_SUCCESS, 0, startTime, "")
}

func extractPushArchive(zipReader *zip.Reader, destinationPath string) (int64, error) {
	treatAsDir, err := shouldTreatDestinationAsDir(destinationPath, len(zipReader.File))
	if err != nil {
		return 0, err
	}

	var totalWritten int64
	for _, archiveFile := range zipReader.File {
		targetPath := destinationPath
		if treatAsDir {
			targetPath = filepath.Join(destinationPath, filepath.Base(archiveFile.Name))
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), os.ModePerm); err != nil {
			return totalWritten, err
		}

		rc, err := archiveFile.Open()
		if err != nil {
			return totalWritten, err
		}

		outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, archiveFile.Mode())
		if err != nil {
			rc.Close()
			return totalWritten, err
		}

		written, copyErr := io.Copy(outFile, rc)
		closeErr := outFile.Close()
		rcErr := rc.Close()
		totalWritten += written
		if copyErr != nil {
			return totalWritten, copyErr
		}
		if closeErr != nil {
			return totalWritten, closeErr
		}
		if rcErr != nil {
			return totalWritten, rcErr
		}
	}

	return totalWritten, nil
}

func shouldTreatDestinationAsDir(destinationPath string, fileCount int) (bool, error) {
	if fileCount > 1 {
		return true, nil
	}

	if strings.HasSuffix(destinationPath, "\\") || strings.HasSuffix(destinationPath, "/") {
		return true, nil
	}

	info, err := os.Stat(destinationPath)
	if err == nil {
		return info.IsDir(), nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}

	return false, nil
}
