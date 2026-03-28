package internal

import (
	"context"
	"log/slog"
	"time"

	"agent-management/server/internal/storage"
)

const defaultNotificationRetryBatchSize = 50

type NotificationRetryProcessor struct {
	logger     *slog.Logger
	appStorage *storage.Storage
	server     *Server
	interval   time.Duration
	batchSize  int
}

func NewNotificationRetryProcessor(logger *slog.Logger, appStorage *storage.Storage, server *Server, interval time.Duration, batchSize int) *NotificationRetryProcessor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if batchSize <= 0 {
		batchSize = defaultNotificationRetryBatchSize
	}
	return &NotificationRetryProcessor{
		logger:     logger,
		appStorage: appStorage,
		server:     server,
		interval:   interval,
		batchSize:  batchSize,
	}
}

func (p *NotificationRetryProcessor) Start(ctx context.Context) {
	if p == nil || p.appStorage == nil || p.appStorage.Notification == nil || p.server == nil {
		return
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		p.ProcessDueDeliveries(ctx)

		select {
		case <-ctx.Done():
			p.logger.Info("Notification retry processor stopped", "reason", ctx.Err())
			return
		case <-ticker.C:
		}
	}
}

func (p *NotificationRetryProcessor) ProcessDueDeliveries(ctx context.Context) {
	now := time.Now()
	deliveries, err := p.appStorage.Notification.ListNotificationDeliveriesForDispatch(ctx, now, p.batchSize)
	if err != nil {
		p.logger.Error("Failed to list notification deliveries for dispatch", "error", err)
		return
	}

	for _, delivery := range deliveries {
		event, err := p.appStorage.Notification.GetNotificationEventByID(ctx, delivery.NotificationEventID)
		if err != nil {
			p.logger.Error("Failed to load notification event for retry", "delivery_id", delivery.ID, "event_id", delivery.NotificationEventID, "error", err)
			continue
		}
		p.server.dispatchNotificationDelivery(ctx, event, delivery)
	}
}
