package web

import (
	"agent-management/server/internal/auth"
	"agent-management/server/internal/events"
	"agent-management/server/internal/notifier"
	"bytes"
	"database/sql"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"agent-management/server/internal/storage"
	"agent-management/server/internal/storage/mocks"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

// setupTestHandlers is a helper function to create all mocks and handlers for testing.
func setupTestHandlers(t *testing.T) (*Handlers, *mocks.MockAgentRepository, *mocks.MockTaskRepository, *mocks.MockLogRepository, *mocks.MockMetricRepository, *mocks.MockNotificationRepository, *events.EventBroker) {
	ctrl := gomock.NewController(t)
	mockAgentRepo := mocks.NewMockAgentRepository(ctrl)
	mockTaskRepo := mocks.NewMockTaskRepository(ctrl)
	mockLogRepo := mocks.NewMockLogRepository(ctrl)
	mockMetricRepo := mocks.NewMockMetricRepository(ctrl)
	mockNotificationRepo := mocks.NewMockNotificationRepository(ctrl)

	mockStorage := &storage.Storage{
		Agent:        mockAgentRepo,
		Task:         mockTaskRepo,
		Log:          mockLogRepo,
		Metric:       mockMetricRepo,
		Notification: mockNotificationRepo,
	}

	var funcMap = template.FuncMap{
		"lower":               strings.ToLower,
		"agentPermissionRole": auth.RoleForAgentPermission,
		"agentLegacyRole":     auth.RoleForAgent,
	}

	templates, err := template.New("").Funcs(funcMap).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	logNotifier := notifier.NewLogNotifier(logger)
	multiNotifier := notifier.NewMultiNotifier(logger, logNotifier)

	broker := events.NewEventBroker(logger)
	go broker.Start()
	t.Cleanup(broker.Stop)

	h := NewHandlers(logger, mockStorage, templates, multiNotifier, broker)
	return h, mockAgentRepo, mockTaskRepo, mockLogRepo, mockMetricRepo, mockNotificationRepo, broker
}

func TestListAgents(t *testing.T) {
	h, mockAgentRepo, _, _, _, _, _ := setupTestHandlers(t)

	t.Run("success", func(t *testing.T) {
		agents := []*storage.Agent{{Hostname: "agent-1"}, {Hostname: "agent-2"}}
		mockAgentRepo.EXPECT().ListAgents(gomock.Any()).Return(agents, nil)

		req := httptest.NewRequest("GET", "/agents", nil)
		rr := httptest.NewRecorder()
		h.listAgents(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Errorf("listAgents handler returned wrong status code: got %v want %v", status, http.StatusOK)
		}
	})
}

func TestListTasks(t *testing.T) {
	h, mockAgentRepo, mockTaskRepo, _, _, _, _ := setupTestHandlers(t)

	t.Run("success", func(t *testing.T) {
		taskID := uuid.New()
		agentID := uuid.New()
		tasks := []*storage.Task{{ID: taskID, AgentID: agentID, Description: sql.NullString{String: "Sync browser profile", Valid: true}}}
		mockAgentRepo.EXPECT().ListAgents(gomock.Any()).Return([]*storage.Agent{{ID: agentID, Hostname: "Dell-17"}}, nil)
		mockTaskRepo.EXPECT().ListTasks(gomock.Any()).Return(tasks, nil)

		req := httptest.NewRequest("GET", "/tasks", nil)
		rr := httptest.NewRecorder()
		h.listTasks(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Errorf("listTasks handler returned wrong status code: got %v want %v", status, http.StatusOK)
		}
		if !strings.Contains(rr.Body.String(), "Dell-17") {
			t.Errorf("expected rendered page to contain agent hostname, got %q", rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "Sync browser profile") {
			t.Errorf("expected rendered page to contain task description, got %q", rr.Body.String())
		}
	})
}

func TestViewTask(t *testing.T) {
	h, _, mockTaskRepo, mockLogRepo, _, mockNotificationRepo, _ := setupTestHandlers(t)
	taskID := uuid.New()

	t.Run("success", func(t *testing.T) {
		task := &storage.Task{ID: taskID}
		result := &storage.TaskResult{TaskID: taskID}
		logs := []*storage.Log{{Message: "log 1"}}

		mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(task, nil)
		mockTaskRepo.EXPECT().GetTaskResultByTaskID(gomock.Any(), taskID).Return(result, nil)
		mockLogRepo.EXPECT().GetLogsByTaskID(gomock.Any(), taskID).Return(logs, nil)
		mockNotificationRepo.EXPECT().ListNotificationEventsByTaskID(gomock.Any(), taskID).Return(nil, nil)

		req := httptest.NewRequest("GET", "/task/view?id="+taskID.String(), nil)
		rr := httptest.NewRecorder()
		h.viewTask(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Errorf("viewTask handler returned wrong status code: got %v want %v", status, http.StatusOK)
		}
	})
}

func TestCreateTask(t *testing.T) {
	h, mockAgentRepo, mockTaskRepo, _, _, _, _ := setupTestHandlers(t)
	agentID := uuid.New()

	// Helper to create multipart requests
	createMultipartRequest := func(values map[string]string) (*http.Request, error) {
		body := new(bytes.Buffer)
		writer := multipart.NewWriter(body)
		for key, val := range values {
			if err := writer.WriteField(key, val); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		req := httptest.NewRequest("POST", "/task/new", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req, nil
	}

	t.Run("success", func(t *testing.T) {
		values := map[string]string{
			"agent_id":              agentID.String(),
			"task_type":             "EXEC_COMMAND",
			"command":               "ls",
			"description":           "List files in home directory",
			"result_contract":       "alert_payload.v1",
			"notification_rule_set": "errors_only",
			"default_destinations":  "telegram, telegram:123456",
		}
		req, err := createMultipartRequest(values)
		if err != nil {
			t.Fatal(err)
		}

		mockTaskRepo.EXPECT().
			CreateTask(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ any, task *storage.Task) error {
				if !task.Description.Valid || task.Description.String != "List files in home directory" {
					t.Fatalf("expected task description to be saved, got %+v", task.Description)
				}
				if !task.ResultContract.Valid || task.ResultContract.String != "alert_payload.v1" {
					t.Fatalf("expected result contract to be saved, got %+v", task.ResultContract)
				}
				if !task.NotificationRuleSet.Valid || task.NotificationRuleSet.String != "errors_only" {
					t.Fatalf("expected notification rule set to be saved, got %+v", task.NotificationRuleSet)
				}
				if len(task.DefaultDestinations) != 2 || task.DefaultDestinations[1] != "telegram:123456" {
					t.Fatalf("expected destinations to be saved, got %+v", task.DefaultDestinations)
				}
				return nil
			})

		rr := httptest.NewRecorder()
		h.createTask(rr, req)

		if status := rr.Code; status != http.StatusSeeOther {
			t.Errorf("createTask handler returned wrong status code: got %v want %v", status, http.StatusSeeOther)
		}
	})

	t.Run("no agent id", func(t *testing.T) {
		values := map[string]string{"task_type": "EXEC_COMMAND"}
		req, err := createMultipartRequest(values)
		if err != nil {
			t.Fatal(err)
		}

		mockAgentRepo.EXPECT().ListAgents(gomock.Any()).Return(nil, nil).AnyTimes()
		mockTaskRepo.EXPECT().ListTasks(gomock.Any()).Return(nil, nil).AnyTimes()

		rr := httptest.NewRecorder()
		h.createTask(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Errorf("createTask with validation error returned wrong status: got %v want %v", status, http.StatusOK)
		}
		// NOTE: We are no longer checking the body because it's a full HTML page
		// and the user does not want to add a specific error div.
	})

	t.Run("storage error", func(t *testing.T) {
		values := map[string]string{
			"agent_id":  agentID.String(),
			"task_type": "EXEC_COMMAND",
			"command":   "ls",
		}
		req, err := createMultipartRequest(values)
		if err != nil {
			t.Fatal(err)
		}

		mockTaskRepo.EXPECT().CreateTask(gomock.Any(), gomock.Any()).Return(errors.New("db error"))

		rr := httptest.NewRecorder()
		h.createTask(rr, req)

		if status := rr.Code; status != http.StatusInternalServerError {
			t.Errorf("createTask with storage error returned wrong status: got %v want %v", status, http.StatusInternalServerError)
		}
	})

	t.Run("forbidden without action role", func(t *testing.T) {
		values := map[string]string{
			"agent_id":  agentID.String(),
			"task_type": "EXEC_COMMAND",
			"command":   "ls",
		}
		req, err := createMultipartRequest(values)
		if err != nil {
			t.Fatal(err)
		}
		req = req.WithContext(auth.WithPrincipal(req.Context(), auth.NewPrincipal("operator", []string{
			auth.RoleForAgent(agentID),
		})))

		rr := httptest.NewRecorder()
		h.createTask(rr, req)

		if status := rr.Code; status != http.StatusForbidden {
			t.Errorf("createTask without action role returned wrong status: got %v want %v", status, http.StatusForbidden)
		}
	})
}

func TestUpdateTaskSchedule(t *testing.T) {
	h, _, mockTaskRepo, _, _, _, _ := setupTestHandlers(t)
	taskID := uuid.New()
	scheduledTime := time.Now().Add(2 * time.Hour).UTC()
	form := url.Values{}
	form.Add("schedule_type", "ONCE")
	form.Add("scheduled_at", scheduledTime.Format("2006-01-02T15:04"))

	t.Run("success", func(t *testing.T) {
		mockTaskRepo.EXPECT().
			UpdateTaskSchedule(
				gomock.Any(),
				taskID,
				sql.NullString{String: "ONCE", Valid: true},
				gomock.Any(),
				sql.NullString{},
				uuid.NullUUID{},
			).
			Return(nil).
			Times(1)

		req := httptest.NewRequest("POST", "/task/"+taskID.String()+"/reschedule", strings.NewReader(form.Encode()))
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", taskID.String())

		rr := httptest.NewRecorder()

		h.handleRescheduleTask(rr, req)

		if status := rr.Code; status != http.StatusSeeOther {
			t.Errorf("updateTaskSchedule handler returned wrong status code: got %v want %v", status, http.StatusSeeOther)
		}
		if loc := rr.Header().Get("Location"); loc != "/task/view?id="+taskID.String() {
			t.Errorf("Expected redirect to %s, got %s", "/task/view?id="+taskID.String(), loc)
		}
	})
}

func TestEventsSSE(t *testing.T) {
	h, _, _, _, _, _, broker := setupTestHandlers(t)

	req := httptest.NewRequest("GET", "/events", nil)
	rr := httptest.NewRecorder()

	go h.sseHandler(rr, req)

	time.Sleep(250 * time.Millisecond)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("events handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	testData := map[string]interface{}{"hello": "world"}
	broker.Broadcast(testData)

	time.Sleep(250 * time.Millisecond)

	bodyString := rr.Body.String()
	expectedEvent := `data: {"hello":"world"}`
	if !strings.Contains(bodyString, expectedEvent) {
		t.Errorf("SSE event not found in response. Got: %q Want to contain: %q", bodyString, expectedEvent)
	}
}

func TestListNotificationEvents(t *testing.T) {
	h, mockAgentRepo, mockTaskRepo, _, _, mockNotificationRepo, _ := setupTestHandlers(t)

	eventID := uuid.New()
	taskID := uuid.New()
	agentID := uuid.New()
	events := []*storage.NotificationEvent{
		{
			ID:        eventID,
			TaskID:    taskID,
			AgentID:   agentID,
			Title:     "Matches found",
			EventType: "file.lines_detected",
			Severity:  "warning",
			Status:    storage.NotificationEventStatusAccepted,
			CreatedAt: time.Now(),
		},
	}

	mockNotificationRepo.EXPECT().ListNotificationEvents(gomock.Any(), 100).Return(events, nil)
	mockAgentRepo.EXPECT().ListAgents(gomock.Any()).Return([]*storage.Agent{{ID: agentID, Hostname: "Dell-17"}}, nil)
	mockTaskRepo.EXPECT().GetTaskByID(gomock.Any(), taskID).Return(&storage.Task{
		ID:          taskID,
		Description: sql.NullString{String: "Analyze result.txt", Valid: true},
		TaskType:    storage.TaskTypeExecPythonScript,
	}, nil)

	req := httptest.NewRequest("GET", "/notifications", nil)
	rr := httptest.NewRecorder()
	h.listNotificationEvents(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("listNotificationEvents handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Matches found") || !strings.Contains(body, "Analyze result.txt") {
		t.Fatalf("expected response to render event title and task description, got %q", body)
	}
}

func TestViewNotificationEvent(t *testing.T) {
	h, _, _, _, _, mockNotificationRepo, _ := setupTestHandlers(t)

	eventID := uuid.New()
	event := &storage.NotificationEvent{
		ID:          eventID,
		TaskID:      uuid.New(),
		AgentID:     uuid.New(),
		Title:       "Matches found",
		Summary:     "Found lines in result.txt",
		EventType:   "file.lines_detected",
		Severity:    "warning",
		Status:      storage.NotificationEventStatusAccepted,
		PayloadJSON: []byte(`{"schema":"alert_payload","summary":"Found lines in result.txt"}`),
		CreatedAt:   time.Now(),
	}
	deliveries := []*storage.NotificationDelivery{{
		ID:                  uuid.New(),
		NotificationEventID: eventID,
		Channel:             "telegram",
		Destination:         "123456",
		Status:              storage.NotificationDeliveryStatusDeadLetter,
		Attempt:             3,
		MaxAttempts:         3,
		ScheduledAt:         time.Now(),
	}}

	mockNotificationRepo.EXPECT().GetNotificationEventByID(gomock.Any(), eventID).Return(event, nil)
	mockNotificationRepo.EXPECT().ListNotificationDeliveriesByEventID(gomock.Any(), eventID).Return(deliveries, nil)

	req := httptest.NewRequest("GET", "/notifications/"+eventID.String(), nil)
	req.SetPathValue("id", eventID.String())
	rr := httptest.NewRecorder()
	h.viewNotificationEvent(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("viewNotificationEvent handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Retry Now") || !strings.Contains(body, "Found lines in result.txt") {
		t.Fatalf("expected response to render delivery retry and payload summary, got %q", body)
	}
}

func TestRetryNotificationDelivery(t *testing.T) {
	h, _, _, _, _, mockNotificationRepo, _ := setupTestHandlers(t)

	deliveryID := uuid.New()
	eventID := uuid.New()
	delivery := &storage.NotificationDelivery{
		ID:                  deliveryID,
		NotificationEventID: eventID,
		Status:              storage.NotificationDeliveryStatusDeadLetter,
		Attempt:             3,
		MaxAttempts:         3,
	}

	mockNotificationRepo.EXPECT().GetNotificationDeliveryByID(gomock.Any(), deliveryID).Return(delivery, nil)
	mockNotificationRepo.EXPECT().ScheduleNotificationDeliveryRetry(gomock.Any(), deliveryID, int32(4), gomock.Any()).Return(nil)

	req := httptest.NewRequest("POST", "/notifications/deliveries/"+deliveryID.String()+"/retry", nil)
	req.SetPathValue("id", deliveryID.String())
	rr := httptest.NewRecorder()
	h.retryNotificationDelivery(rr, req)

	if status := rr.Code; status != http.StatusSeeOther {
		t.Fatalf("retryNotificationDelivery handler returned wrong status code: got %v want %v", status, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/notifications/"+eventID.String() {
		t.Fatalf("expected redirect to event details, got %s", loc)
	}
}

func TestRetryNotificationDeliveryRejectsIneligibleStatus(t *testing.T) {
	h, _, _, _, _, mockNotificationRepo, _ := setupTestHandlers(t)

	deliveryID := uuid.New()
	delivery := &storage.NotificationDelivery{
		ID:                  deliveryID,
		NotificationEventID: uuid.New(),
		Status:              storage.NotificationDeliveryStatusSent,
		Attempt:             1,
		MaxAttempts:         3,
	}

	mockNotificationRepo.EXPECT().GetNotificationDeliveryByID(gomock.Any(), deliveryID).Return(delivery, nil)

	req := httptest.NewRequest("POST", "/notifications/deliveries/"+deliveryID.String()+"/retry", nil)
	req.SetPathValue("id", deliveryID.String())
	rr := httptest.NewRecorder()
	h.retryNotificationDelivery(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Fatalf("expected bad request for ineligible retry status, got %v", status)
	}
}

func TestListAgentsFiltersByPrincipal(t *testing.T) {
	h, mockAgentRepo, _, _, _, _, _ := setupTestHandlers(t)

	allowedAgentID := uuid.New()
	blockedAgentID := uuid.New()
	mockAgentRepo.EXPECT().ListAgents(gomock.Any()).Return([]*storage.Agent{
		{ID: allowedAgentID, Hostname: "allowed-agent"},
		{ID: blockedAgentID, Hostname: "blocked-agent"},
	}, nil)

	req := httptest.NewRequest("GET", "/agents", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.NewPrincipal("viewer", []string{
		auth.RoleForAgent(allowedAgentID),
	})))
	rr := httptest.NewRecorder()

	h.listAgents(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("listAgents returned wrong status code: got %v want %v", status, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "allowed-agent") {
		t.Fatalf("expected allowed agent in response, got %q", body)
	}
	if strings.Contains(body, "blocked-agent") {
		t.Fatalf("did not expect blocked agent in response, got %q", body)
	}
}
