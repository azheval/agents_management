package internal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"agent-management/server/internal/storage"

	"github.com/google/uuid"
)

const (
	alertPayloadSchema       = "alert_payload"
	alertPayloadVersion      = "1"
	alertPayloadPrefix       = "ALERT_PAYLOAD:"
	defaultDedupWindowSecond = 3600
)

type alertPayload struct {
	Schema    string             `json:"schema"`
	Version   string             `json:"version"`
	Notify    bool               `json:"notify"`
	EventType string             `json:"event_type"`
	Severity  string             `json:"severity"`
	Title     string             `json:"title"`
	Summary   string             `json:"summary"`
	Source    alertPayloadSource `json:"source"`
	Details   []string           `json:"details"`
	Metrics   map[string]any     `json:"metrics"`
	Labels    map[string]string  `json:"labels"`
	Dedup     alertPayloadDedup  `json:"dedup"`
}

type alertPayloadSource struct {
	Kind           string `json:"kind"`
	Path           string `json:"path"`
	ProducerTaskID string `json:"producer_task_id"`
}

type alertPayloadDedup struct {
	Scope         string   `json:"scope"`
	WindowSeconds int32    `json:"window_seconds"`
	KeyMaterial   []string `json:"key_material"`
}

func (s *Server) evaluateAndStoreNotificationEvent(ctx context.Context, task *storage.Task, result *storage.TaskResult) {
	if s.storage == nil || s.storage.Notification == nil {
		return
	}
	if task != nil && task.ResultContract.Valid && task.ResultContract.String != "" && task.ResultContract.String != "alert_payload.v1" {
		return
	}
	if !result.Output.Valid || strings.TrimSpace(result.Output.String) == "" {
		return
	}

	payloadBytes, payload, err := parseAlertPayload(result.Output.String)
	if err != nil {
		s.logger.Warn("Failed to parse alert payload", "task_id", task.ID, "error", err)
		return
	}
	if payload == nil {
		return
	}

	event := buildNotificationEvent(task, result, payloadBytes, payload)
	if err := s.storage.Notification.CreateNotificationEvent(ctx, event); err != nil {
		s.logger.Error("Failed to store notification event", "task_id", task.ID, "error", err)
		return
	}

	s.logger.Info(
		"Stored notification event",
		"task_id", task.ID,
		"event_id", event.ID,
		"event_type", event.EventType,
		"severity", event.Severity,
	)

	s.processNotificationEvent(ctx, task, event)
}

func parseAlertPayload(output string) ([]byte, *alertPayload, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, nil, nil
	}

	var payloadJSON string
	switch {
	case strings.HasPrefix(trimmed, alertPayloadPrefix):
		payloadJSON = strings.TrimSpace(strings.TrimPrefix(trimmed, alertPayloadPrefix))
	case strings.HasPrefix(trimmed, "{"):
		payloadJSON = trimmed
	default:
		return nil, nil, nil
	}

	var payload alertPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, nil, fmt.Errorf("invalid json: %w", err)
	}
	if payload.Schema != alertPayloadSchema || payload.Version != alertPayloadVersion {
		return nil, nil, nil
	}
	if err := validateAlertPayload(payload); err != nil {
		return nil, nil, err
	}

	return []byte(payloadJSON), &payload, nil
}

func validateAlertPayload(payload alertPayload) error {
	if payload.EventType == "" {
		return fmt.Errorf("event_type is required")
	}
	if payload.Severity == "" {
		return fmt.Errorf("severity is required")
	}
	if payload.Title == "" {
		return fmt.Errorf("title is required")
	}
	if payload.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if payload.Source.Kind == "" {
		return fmt.Errorf("source.kind is required")
	}
	if len(payload.Details) > 20 {
		return fmt.Errorf("details exceeds limit")
	}

	switch payload.Severity {
	case "info", "warning", "error", "critical":
	default:
		return fmt.Errorf("unsupported severity %q", payload.Severity)
	}

	switch payload.Source.Kind {
	case "file", "stdout", "stderr", "task_output", "external":
	default:
		return fmt.Errorf("unsupported source.kind %q", payload.Source.Kind)
	}

	return nil
}

func buildNotificationEvent(task *storage.Task, result *storage.TaskResult, payloadBytes []byte, payload *alertPayload) *storage.NotificationEvent {
	sourcePath := sql.NullString{String: payload.Source.Path, Valid: payload.Source.Path != ""}
	sourceRef := sql.NullString{String: payload.Source.ProducerTaskID, Valid: payload.Source.ProducerTaskID != ""}
	windowSeconds := payload.Dedup.WindowSeconds
	if windowSeconds <= 0 {
		windowSeconds = defaultDedupWindowSecond
	}

	return &storage.NotificationEvent{
		ID:                 uuid.New(),
		TaskID:             task.ID,
		AgentID:            result.AgentID,
		PrerequisiteTaskID: task.PrerequisiteTaskID,
		EventType:          payload.EventType,
		Severity:           payload.Severity,
		Title:              payload.Title,
		Summary:            payload.Summary,
		SourceKind:         payload.Source.Kind,
		SourcePath:         sourcePath,
		SourceRef:          sourceRef,
		PayloadJSON:        payloadBytes,
		DedupKey:           sql.NullString{String: buildDedupKey(payload), Valid: true},
		DedupWindowSeconds: sql.NullInt32{Int32: windowSeconds, Valid: true},
		Status:             storage.NotificationEventStatusDetected,
		CreatedAt:          time.Now(),
	}
}

func buildDedupKey(payload *alertPayload) string {
	parts := []string{
		payload.EventType,
		payload.Source.Kind,
		payload.Source.Path,
		payload.Source.ProducerTaskID,
	}

	if len(payload.Labels) > 0 {
		keys := make([]string, 0, len(payload.Labels))
		for key := range payload.Labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, key+"="+payload.Labels[key])
		}
	}

	if len(payload.Dedup.KeyMaterial) > 0 {
		parts = append(parts, payload.Dedup.KeyMaterial...)
	} else {
		parts = append(parts, normalizeDedupText(payload.Summary))
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeDedupText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}
