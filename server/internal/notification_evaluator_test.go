package internal

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"agent-management/server/internal/events"
	"agent-management/server/internal/notifier"
	"agent-management/server/internal/storage"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type stubNotificationRepo struct {
	createdEvents      []*storage.NotificationEvent
	updatedEventStatus []storage.NotificationEventStatus
	createdDeliveries  []*storage.NotificationDelivery
}

func (s *stubNotificationRepo) CreateNotificationEvent(ctx context.Context, event *storage.NotificationEvent) error {
	s.createdEvents = append(s.createdEvents, event)
	return nil
}

func (s *stubNotificationRepo) UpdateNotificationEventStatus(ctx context.Context, id uuid.UUID, status storage.NotificationEventStatus) error {
	s.updatedEventStatus = append(s.updatedEventStatus, status)
	for _, event := range s.createdEvents {
		if event.ID == id {
			event.Status = status
		}
	}
	return nil
}

func (s *stubNotificationRepo) GetNotificationEventByID(ctx context.Context, id uuid.UUID) (*storage.NotificationEvent, error) {
	for _, event := range s.createdEvents {
		if event.ID == id {
			return event, nil
		}
	}
	return nil, nil
}

func (s *stubNotificationRepo) FindLatestNotificationEventByDedupKeySince(ctx context.Context, dedupKey string, since time.Time, excludeID uuid.UUID) (*storage.NotificationEvent, error) {
	for i := len(s.createdEvents) - 1; i >= 0; i-- {
		event := s.createdEvents[i]
		if event.ID == excludeID {
			continue
		}
		if event.DedupKey.Valid && event.DedupKey.String == dedupKey && event.CreatedAt.After(since) {
			return event, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *stubNotificationRepo) ListNotificationEvents(ctx context.Context, limit int) ([]*storage.NotificationEvent, error) {
	return nil, nil
}

func (s *stubNotificationRepo) ListNotificationEventsByTaskID(ctx context.Context, taskID uuid.UUID) ([]*storage.NotificationEvent, error) {
	events := make([]*storage.NotificationEvent, 0)
	for _, event := range s.createdEvents {
		if event.TaskID == taskID {
			events = append(events, event)
		}
	}
	return events, nil
}

func (s *stubNotificationRepo) CreateNotificationDelivery(ctx context.Context, delivery *storage.NotificationDelivery) error {
	s.createdDeliveries = append(s.createdDeliveries, delivery)
	return nil
}

func (s *stubNotificationRepo) UpdateNotificationDeliveryStatus(ctx context.Context, id uuid.UUID, attempt int32, status storage.NotificationDeliveryStatus, providerMessageID sql.NullString, providerResponseJSON []byte, errorMessage sql.NullString, lastErrorCode sql.NullString, sentAt sql.NullTime, nextRetryAt sql.NullTime) error {
	for _, delivery := range s.createdDeliveries {
		if delivery.ID == id {
			delivery.Attempt = attempt
			delivery.Status = status
			delivery.ProviderMessageID = providerMessageID
			delivery.ProviderResponseJSON = providerResponseJSON
			delivery.ErrorMessage = errorMessage
			delivery.LastErrorCode = lastErrorCode
			delivery.SentAt = sentAt
			delivery.NextRetryAt = nextRetryAt
		}
	}
	return nil
}

func (s *stubNotificationRepo) GetNotificationDeliveryByID(ctx context.Context, id uuid.UUID) (*storage.NotificationDelivery, error) {
	for _, delivery := range s.createdDeliveries {
		if delivery.ID == id {
			return delivery, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *stubNotificationRepo) ListNotificationDeliveriesByEventID(ctx context.Context, eventID uuid.UUID) ([]*storage.NotificationDelivery, error) {
	deliveries := make([]*storage.NotificationDelivery, 0)
	for _, delivery := range s.createdDeliveries {
		if delivery.NotificationEventID == eventID {
			deliveries = append(deliveries, delivery)
		}
	}
	return deliveries, nil
}

func (s *stubNotificationRepo) ListNotificationDeliveriesForDispatch(ctx context.Context, now time.Time, limit int) ([]*storage.NotificationDelivery, error) {
	deliveries := make([]*storage.NotificationDelivery, 0)
	for _, delivery := range s.createdDeliveries {
		if delivery.Status == storage.NotificationDeliveryStatusPending && !delivery.ScheduledAt.After(now) {
			deliveries = append(deliveries, delivery)
			continue
		}
		if delivery.Status == storage.NotificationDeliveryStatusRetryScheduled && delivery.NextRetryAt.Valid && !delivery.NextRetryAt.Time.After(now) {
			deliveries = append(deliveries, delivery)
		}
	}
	if len(deliveries) > limit {
		return deliveries[:limit], nil
	}
	return deliveries, nil
}

func (s *stubNotificationRepo) ScheduleNotificationDeliveryRetry(ctx context.Context, id uuid.UUID, maxAttempts int32, nextRetryAt time.Time) error {
	for _, delivery := range s.createdDeliveries {
		if delivery.ID == id {
			delivery.Status = storage.NotificationDeliveryStatusRetryScheduled
			delivery.MaxAttempts = maxAttempts
			delivery.NextRetryAt = sql.NullTime{Time: nextRetryAt, Valid: true}
			delivery.ErrorMessage = sql.NullString{}
			delivery.LastErrorCode = sql.NullString{}
			delivery.ProviderResponseJSON = nil
			delivery.SentAt = sql.NullTime{}
			return nil
		}
	}
	return sql.ErrNoRows
}

type stubDeliveryAdapter struct {
	channel           string
	providerMessageID string
	providerResponse  []byte
	err               error
}

func (s *stubDeliveryAdapter) Channel() string {
	return s.channel
}

func (s *stubDeliveryAdapter) DeliverNotificationEvent(ctx context.Context, event *storage.NotificationEvent, delivery *storage.NotificationDelivery) (string, []byte, error) {
	return s.providerMessageID, s.providerResponse, s.err
}

func TestParseAlertPayload(t *testing.T) {
	t.Run("parses direct json payload", func(t *testing.T) {
		output := `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"warning","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`
		payloadBytes, payload, err := parseAlertPayload(output)
		if err != nil {
			t.Fatalf("parseAlertPayload returned error: %v", err)
		}
		if payload == nil {
			t.Fatal("expected payload to be parsed")
		}
		if string(payloadBytes) != output {
			t.Fatalf("expected payload bytes to match original output")
		}
		if payload.EventType != "file.lines_detected" {
			t.Fatalf("unexpected event type: %s", payload.EventType)
		}
	})

	t.Run("parses prefixed payload", func(t *testing.T) {
		output := `ALERT_PAYLOAD:{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"warning","title":"Matches found","summary":"Found lines","source":{"kind":"file"}}`
		_, payload, err := parseAlertPayload(output)
		if err != nil {
			t.Fatalf("parseAlertPayload returned error: %v", err)
		}
		if payload == nil {
			t.Fatal("expected payload to be parsed")
		}
	})

	t.Run("ignores non alert payload", func(t *testing.T) {
		payloadBytes, payload, err := parseAlertPayload("plain stdout line")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if payloadBytes != nil || payload != nil {
			t.Fatal("expected payload to be ignored")
		}
	})

	t.Run("returns error for invalid alert payload json", func(t *testing.T) {
		_, _, err := parseAlertPayload(`{"schema":"alert_payload","version":"1"`)
		if err == nil {
			t.Fatal("expected invalid json to return error")
		}
	})
}

func TestEvaluateAndStoreNotificationEvent(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{
		Notification: notificationRepo,
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)

	taskID := uuid.New()
	agentID := uuid.New()

	task := &storage.Task{
		ID:                  taskID,
		AgentID:             agentID,
		PrerequisiteTaskID:  uuid.NullUUID{UUID: uuid.New(), Valid: true},
		ResultContract:      sql.NullString{String: "alert_payload.v1", Valid: true},
		NotificationRuleSet: sql.NullString{String: "default", Valid: true},
	}
	result := &storage.TaskResult{
		TaskID:  taskID,
		AgentID: agentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"warning","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"},"labels":{"service":"payments"}}`,
			Valid:  true,
		},
	}

	server.evaluateAndStoreNotificationEvent(context.Background(), task, result)

	if len(notificationRepo.createdEvents) != 1 {
		t.Fatalf("expected 1 notification event, got %d", len(notificationRepo.createdEvents))
	}

	event := notificationRepo.createdEvents[0]
	if event.EventType != "file.lines_detected" {
		t.Fatalf("unexpected event type: %s", event.EventType)
	}
	if !event.DedupKey.Valid || event.DedupKey.String == "" {
		t.Fatal("expected dedup key to be generated")
	}
	if event.Status != storage.NotificationEventStatusDetected {
		t.Fatalf("unexpected event status: %s", event.Status)
	}
}

func TestEvaluateAndRouteNotificationEvent(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{
		Notification: notificationRepo,
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationPipeline(
		NewNotificationRuleEngine(NewDefaultNotificationPolicy()),
		NewNotificationRouter(NotificationRoute{
			Channel:     "telegram",
			Destination: "123456",
			MaxAttempts: 5,
		}),
	)
	server.SetNotificationDeliveryDispatcher(NewNotificationDeliveryDispatcher(logger, &stubDeliveryAdapter{
		channel:           "telegram",
		providerMessageID: "42",
		providerResponse:  []byte(`{"ok":true}`),
	}))

	taskID := uuid.New()
	agentID := uuid.New()

	task := &storage.Task{ID: taskID, AgentID: agentID}
	result := &storage.TaskResult{
		TaskID:  taskID,
		AgentID: agentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"error","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`,
			Valid:  true,
		},
	}

	server.evaluateAndStoreNotificationEvent(context.Background(), task, result)

	if len(notificationRepo.createdDeliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(notificationRepo.createdDeliveries))
	}
	delivery := notificationRepo.createdDeliveries[0]
	if got := delivery.Channel; got != "telegram" {
		t.Fatalf("unexpected channel: %s", got)
	}
	if delivery.Status != storage.NotificationDeliveryStatusSent {
		t.Fatalf("expected sent delivery status, got %s", delivery.Status)
	}
	if delivery.Attempt != 1 {
		t.Fatalf("expected attempt 1, got %d", delivery.Attempt)
	}
	if !delivery.ProviderMessageID.Valid || delivery.ProviderMessageID.String != "42" {
		t.Fatal("expected provider message id to be saved")
	}
	if event := notificationRepo.createdEvents[0]; event.Status != storage.NotificationEventStatusAccepted {
		t.Fatalf("expected accepted event status, got %s", event.Status)
	}
}

func TestEvaluateAndSuppressDuplicateNotificationEvent(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{
		Notification: notificationRepo,
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationPipeline(
		NewNotificationRuleEngine(NewDefaultNotificationPolicy()),
		NewNotificationRouter(NotificationRoute{
			Channel:     "telegram",
			Destination: "123456",
			MaxAttempts: 3,
		}),
	)
	server.SetNotificationDeliveryDispatcher(NewNotificationDeliveryDispatcher(logger, &stubDeliveryAdapter{
		channel: "telegram",
	}))

	firstTaskID := uuid.New()
	agentID := uuid.New()
	firstTask := &storage.Task{ID: firstTaskID, AgentID: agentID}
	firstResult := &storage.TaskResult{
		TaskID:  firstTaskID,
		AgentID: agentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"warning","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`,
			Valid:  true,
		},
	}
	server.evaluateAndStoreNotificationEvent(context.Background(), firstTask, firstResult)

	secondTaskID := uuid.New()
	secondTask := &storage.Task{ID: secondTaskID, AgentID: agentID}
	secondResult := &storage.TaskResult{
		TaskID:  secondTaskID,
		AgentID: agentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"warning","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`,
			Valid:  true,
		},
	}
	server.evaluateAndStoreNotificationEvent(context.Background(), secondTask, secondResult)

	if len(notificationRepo.createdEvents) != 2 {
		t.Fatalf("expected 2 events, got %d", len(notificationRepo.createdEvents))
	}
	if notificationRepo.createdEvents[1].Status != storage.NotificationEventStatusSuppressed {
		t.Fatalf("expected duplicate event to be suppressed, got %s", notificationRepo.createdEvents[1].Status)
	}
	if len(notificationRepo.createdDeliveries) != 1 {
		t.Fatalf("expected only first event to create delivery, got %d", len(notificationRepo.createdDeliveries))
	}
}

func TestEvaluateAndSuppressNotificationEvent(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{
		Notification: notificationRepo,
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationPipeline(
		NewNotificationRuleEngine(NewDefaultNotificationPolicy()),
		NewNotificationRouter(NotificationRoute{
			Channel:     "telegram",
			Destination: "123456",
			MaxAttempts: 3,
		}),
	)
	server.SetNotificationDeliveryDispatcher(NewNotificationDeliveryDispatcher(logger, &stubDeliveryAdapter{
		channel: "telegram",
	}))

	taskID := uuid.New()
	agentID := uuid.New()

	task := &storage.Task{ID: taskID, AgentID: agentID}
	result := &storage.TaskResult{
		TaskID:  taskID,
		AgentID: agentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":false,"event_type":"file.lines_detected","severity":"warning","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`,
			Valid:  true,
		},
	}

	server.evaluateAndStoreNotificationEvent(context.Background(), task, result)

	if len(notificationRepo.createdDeliveries) != 0 {
		t.Fatalf("expected 0 deliveries, got %d", len(notificationRepo.createdDeliveries))
	}
	if event := notificationRepo.createdEvents[0]; event.Status != storage.NotificationEventStatusSuppressed {
		t.Fatalf("expected suppressed event status, got %s", event.Status)
	}
}

func TestEvaluateAndFailNotificationDelivery(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{
		Notification: notificationRepo,
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationPipeline(
		NewNotificationRuleEngine(NewDefaultNotificationPolicy()),
		NewNotificationRouter(NotificationRoute{
			Channel:     "telegram",
			Destination: "123456",
			MaxAttempts: 3,
		}),
	)
	server.SetNotificationDeliveryDispatcher(NewNotificationDeliveryDispatcher(logger, &stubDeliveryAdapter{
		channel: "telegram",
		err:     errors.New("telegram unavailable"),
	}))

	taskID := uuid.New()
	agentID := uuid.New()

	task := &storage.Task{ID: taskID, AgentID: agentID}
	result := &storage.TaskResult{
		TaskID:  taskID,
		AgentID: agentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"critical","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`,
			Valid:  true,
		},
	}

	server.evaluateAndStoreNotificationEvent(context.Background(), task, result)

	if len(notificationRepo.createdDeliveries) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(notificationRepo.createdDeliveries))
	}
	delivery := notificationRepo.createdDeliveries[0]
	if delivery.Status != storage.NotificationDeliveryStatusRetryScheduled {
		t.Fatalf("expected retry_scheduled delivery status, got %s", delivery.Status)
	}
	if delivery.Attempt != 1 {
		t.Fatalf("expected attempt 1, got %d", delivery.Attempt)
	}
	if !delivery.ErrorMessage.Valid {
		t.Fatal("expected delivery error to be stored")
	}
	if !delivery.NextRetryAt.Valid {
		t.Fatal("expected next retry timestamp to be stored")
	}
}

func TestNotificationDeliveryMovesToDeadLetter(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, &storage.Storage{Notification: notificationRepo}, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationDeliveryDispatcher(NewNotificationDeliveryDispatcher(logger, &stubDeliveryAdapter{
		channel: "telegram",
		err:     errors.New("telegram unavailable"),
	}))

	event := &storage.NotificationEvent{
		ID:         uuid.New(),
		TaskID:     uuid.New(),
		AgentID:    uuid.New(),
		EventType:  "file.lines_detected",
		Severity:   "critical",
		Title:      "Matches found",
		Summary:    "Found lines",
		SourceKind: "file",
	}
	delivery := &storage.NotificationDelivery{
		ID:                  uuid.New(),
		NotificationEventID: event.ID,
		Channel:             "telegram",
		Destination:         "123456",
		Status:              storage.NotificationDeliveryStatusRetryScheduled,
		Attempt:             2,
		MaxAttempts:         3,
	}
	notificationRepo.createdEvents = append(notificationRepo.createdEvents, event)
	notificationRepo.createdDeliveries = append(notificationRepo.createdDeliveries, delivery)

	server.dispatchNotificationDelivery(context.Background(), event, delivery)

	if delivery.Status != storage.NotificationDeliveryStatusDeadLetter {
		t.Fatalf("expected dead_letter status, got %s", delivery.Status)
	}
	if delivery.Attempt != 3 {
		t.Fatalf("expected attempt 3, got %d", delivery.Attempt)
	}
	if delivery.NextRetryAt.Valid {
		t.Fatal("expected no next retry for dead letter")
	}
}

func TestNotificationRetryProcessorProcessesDueDeliveries(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, &storage.Storage{Notification: notificationRepo}, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationDeliveryDispatcher(NewNotificationDeliveryDispatcher(logger, &stubDeliveryAdapter{
		channel:           "telegram",
		providerMessageID: "99",
		providerResponse:  []byte(`{"ok":true}`),
	}))

	event := &storage.NotificationEvent{
		ID:         uuid.New(),
		TaskID:     uuid.New(),
		AgentID:    uuid.New(),
		EventType:  "file.lines_detected",
		Severity:   "warning",
		Title:      "Matches found",
		Summary:    "Found lines",
		SourceKind: "file",
	}
	delivery := &storage.NotificationDelivery{
		ID:                  uuid.New(),
		NotificationEventID: event.ID,
		Channel:             "telegram",
		Destination:         "123456",
		Status:              storage.NotificationDeliveryStatusRetryScheduled,
		Attempt:             1,
		MaxAttempts:         3,
		NextRetryAt:         sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true},
	}
	notificationRepo.createdEvents = append(notificationRepo.createdEvents, event)
	notificationRepo.createdDeliveries = append(notificationRepo.createdDeliveries, delivery)

	processor := NewNotificationRetryProcessor(logger, &storage.Storage{Notification: notificationRepo}, server, time.Minute, 10)
	processor.ProcessDueDeliveries(context.Background())

	if delivery.Status != storage.NotificationDeliveryStatusSent {
		t.Fatalf("expected sent status after retry processor, got %s", delivery.Status)
	}
	if delivery.Attempt != 2 {
		t.Fatalf("expected attempt 2 after retry, got %d", delivery.Attempt)
	}
}

func TestNotificationRuleSetDisabledSuppressesEvent(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{Notification: notificationRepo}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationPipeline(
		NewNotificationRuleEngine(NewDefaultNotificationPolicy()),
		NewNotificationRouter(NotificationRoute{Channel: "telegram", Destination: "123456", MaxAttempts: 3}),
	)

	task := &storage.Task{
		ID:                  uuid.New(),
		AgentID:             uuid.New(),
		ResultContract:      sql.NullString{String: "alert_payload.v1", Valid: true},
		NotificationRuleSet: sql.NullString{String: "disabled", Valid: true},
	}
	result := &storage.TaskResult{
		TaskID:  task.ID,
		AgentID: task.AgentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"critical","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`,
			Valid:  true,
		},
	}

	server.evaluateAndStoreNotificationEvent(context.Background(), task, result)

	if len(notificationRepo.createdDeliveries) != 0 {
		t.Fatalf("expected no deliveries, got %d", len(notificationRepo.createdDeliveries))
	}
	if notificationRepo.createdEvents[0].Status != storage.NotificationEventStatusSuppressed {
		t.Fatalf("expected suppressed status, got %s", notificationRepo.createdEvents[0].Status)
	}
}

func TestTaskDefaultDestinationsOverrideRouterDestination(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{Notification: notificationRepo}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)
	server.SetNotificationPipeline(
		NewNotificationRuleEngine(NewDefaultNotificationPolicy()),
		NewNotificationRouter(NotificationRoute{Channel: "telegram", Destination: "999", MaxAttempts: 3}),
	)
	server.SetNotificationDeliveryDispatcher(NewNotificationDeliveryDispatcher(logger, &stubDeliveryAdapter{
		channel:           "telegram",
		providerMessageID: "100",
		providerResponse:  []byte(`{"ok":true}`),
	}))

	task := &storage.Task{
		ID:                  uuid.New(),
		AgentID:             uuid.New(),
		ResultContract:      sql.NullString{String: "alert_payload.v1", Valid: true},
		DefaultDestinations: pq.StringArray{"telegram:123456"},
	}
	result := &storage.TaskResult{
		TaskID:  task.ID,
		AgentID: task.AgentID,
		Output: sql.NullString{
			String: `{"schema":"alert_payload","version":"1","notify":true,"event_type":"file.lines_detected","severity":"warning","title":"Matches found","summary":"Found lines","source":{"kind":"file","path":"result.txt"}}`,
			Valid:  true,
		},
	}

	server.evaluateAndStoreNotificationEvent(context.Background(), task, result)

	if len(notificationRepo.createdDeliveries) != 1 {
		t.Fatalf("expected one delivery, got %d", len(notificationRepo.createdDeliveries))
	}
	if notificationRepo.createdDeliveries[0].Destination != "123456" {
		t.Fatalf("expected overridden destination, got %s", notificationRepo.createdDeliveries[0].Destination)
	}
}

func TestEvaluateInvalidAlertPayloadDoesNotCreateEvent(t *testing.T) {
	notificationRepo := &stubNotificationRepo{}
	appStorage := &storage.Storage{Notification: notificationRepo}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)
	broker := events.NewEventBroker(logger)
	go broker.Start()
	defer broker.Stop()

	server := NewServer(logger, appStorage, multiNotifier, make(chan uuid.UUID, 1), broker)

	task := &storage.Task{
		ID:             uuid.New(),
		AgentID:        uuid.New(),
		ResultContract: sql.NullString{String: "alert_payload.v1", Valid: true},
	}
	result := &storage.TaskResult{
		TaskID:  task.ID,
		AgentID: task.AgentID,
		Output:  sql.NullString{String: `{"schema":"alert_payload","version":"1"`, Valid: true},
	}

	server.evaluateAndStoreNotificationEvent(context.Background(), task, result)

	if len(notificationRepo.createdEvents) != 0 {
		t.Fatalf("expected invalid payload not to create event, got %d", len(notificationRepo.createdEvents))
	}
	if len(notificationRepo.createdDeliveries) != 0 {
		t.Fatalf("expected invalid payload not to create deliveries, got %d", len(notificationRepo.createdDeliveries))
	}
}
