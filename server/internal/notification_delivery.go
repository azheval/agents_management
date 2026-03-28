package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"agent-management/server/internal/storage"
)

type NotificationDeliveryAdapter interface {
	Channel() string
	DeliverNotificationEvent(ctx context.Context, event *storage.NotificationEvent, delivery *storage.NotificationDelivery) (string, []byte, error)
}

type NotificationDeliveryDispatcher struct {
	logger   *slog.Logger
	adapters map[string]NotificationDeliveryAdapter
}

func NewNotificationDeliveryDispatcher(logger *slog.Logger, adapters ...NotificationDeliveryAdapter) *NotificationDeliveryDispatcher {
	dispatcher := &NotificationDeliveryDispatcher{
		logger:   logger,
		adapters: make(map[string]NotificationDeliveryAdapter, len(adapters)),
	}
	for _, adapter := range adapters {
		if adapter == nil || adapter.Channel() == "" {
			continue
		}
		dispatcher.adapters[adapter.Channel()] = adapter
	}
	return dispatcher
}

func (d *NotificationDeliveryDispatcher) Dispatch(ctx context.Context, event *storage.NotificationEvent, delivery *storage.NotificationDelivery) (string, []byte, error) {
	if event == nil || delivery == nil {
		return "", nil, fmt.Errorf("event and delivery are required")
	}

	adapter, ok := d.adapters[delivery.Channel]
	if !ok {
		return "", nil, fmt.Errorf("no delivery adapter configured for channel %q", delivery.Channel)
	}

	return adapter.DeliverNotificationEvent(ctx, event, delivery)
}

func (s *Server) dispatchNotificationDelivery(ctx context.Context, event *storage.NotificationEvent, delivery *storage.NotificationDelivery) {
	if s.storage == nil || s.storage.Notification == nil || s.notificationDeliveryDispatcher == nil {
		return
	}

	attempt := delivery.Attempt + 1
	providerMessageIDValue, providerResponse, err := s.notificationDeliveryDispatcher.Dispatch(ctx, event, delivery)
	if err != nil {
		s.handleNotificationDeliveryFailure(ctx, event, delivery, attempt, err)
		return
	}

	providerMessageID := sql.NullString{}
	if providerMessageIDValue != "" {
		providerMessageID = sql.NullString{String: providerMessageIDValue, Valid: true}
	}
	if len(providerResponse) == 0 {
		providerResponse = []byte("{}")
	}
	sentAt := sql.NullTime{Time: time.Now(), Valid: true}
	if err := s.storage.Notification.UpdateNotificationDeliveryStatus(
		ctx,
		delivery.ID,
		attempt,
		storage.NotificationDeliveryStatusSent,
		providerMessageID,
		providerResponse,
		sql.NullString{},
		sql.NullString{},
		sentAt,
		sql.NullTime{},
	); err != nil {
		s.logger.Error("Failed to persist sent delivery status", "delivery_id", delivery.ID, "error", err)
		return
	}

	delivery.Attempt = attempt
	delivery.Status = storage.NotificationDeliveryStatusSent
	delivery.ProviderMessageID = providerMessageID
	delivery.ProviderResponseJSON = providerResponse
	delivery.SentAt = sentAt
	s.logger.Info("Notification delivery sent", "event_id", event.ID, "delivery_id", delivery.ID, "channel", delivery.Channel)
}

func (s *Server) handleNotificationDeliveryFailure(ctx context.Context, event *storage.NotificationEvent, delivery *storage.NotificationDelivery, attempt int32, err error) {
	providerResponse := marshalDeliveryError(err)
	status := storage.NotificationDeliveryStatusRetryScheduled
	nextRetryAt := sql.NullTime{Time: time.Now().Add(notificationRetryBackoff(attempt)), Valid: true}
	if attempt >= delivery.MaxAttempts {
		status = storage.NotificationDeliveryStatusDeadLetter
		nextRetryAt = sql.NullTime{}
	}

	updateErr := s.storage.Notification.UpdateNotificationDeliveryStatus(
		ctx,
		delivery.ID,
		attempt,
		status,
		sql.NullString{},
		providerResponse,
		sql.NullString{String: err.Error(), Valid: true},
		sql.NullString{String: notificationDeliveryErrorCode(err), Valid: true},
		sql.NullTime{},
		nextRetryAt,
	)
	if updateErr != nil {
		s.logger.Error("Failed to persist failed delivery status", "delivery_id", delivery.ID, "error", updateErr)
		return
	}

	delivery.Attempt = attempt
	delivery.Status = status
	delivery.ErrorMessage = sql.NullString{String: err.Error(), Valid: true}
	delivery.LastErrorCode = sql.NullString{String: notificationDeliveryErrorCode(err), Valid: true}
	delivery.NextRetryAt = nextRetryAt
	if status == storage.NotificationDeliveryStatusDeadLetter {
		s.logger.Error("Notification delivery moved to dead letter", "event_id", event.ID, "delivery_id", delivery.ID, "channel", delivery.Channel, "error", err)
		return
	}
	s.logger.Warn("Notification delivery scheduled for retry", "event_id", event.ID, "delivery_id", delivery.ID, "channel", delivery.Channel, "attempt", attempt, "next_retry_at", nextRetryAt.Time, "error", err)
}

func notificationRetryBackoff(attempt int32) time.Duration {
	if attempt <= 1 {
		return time.Minute
	}

	backoff := time.Minute * time.Duration(1<<(attempt-1))
	if backoff > time.Hour {
		return time.Hour
	}
	return backoff
}

func notificationDeliveryErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELLED"
	}
	return "DELIVERY_ERROR"
}

func marshalDeliveryError(err error) []byte {
	payload, marshalErr := json.Marshal(map[string]string{
		"error": err.Error(),
	})
	if marshalErr != nil {
		return []byte(`{"error":"failed to marshal delivery error"}`)
	}
	return payload
}
