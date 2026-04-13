package web

import (
	"agent-management/server/internal/auth"
	"agent-management/server/internal/events"
	"agent-management/server/internal/notifier"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-management/server/internal/storage"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type TaskDetailPageData struct {
	Task               *storage.Task
	Result             *storage.TaskResult
	Logs               []*storage.Log
	NotificationEvents []*storage.NotificationEvent
}

type NewTaskPageData struct {
	Agents          []*storage.Agent
	SelectableTasks []struct {
		ID          string
		Description string
	}
	Error string
}

type AgentMetricsPageData struct {
	Agent       *storage.Agent
	ChartJSData template.JS
	Error       string
}

type TasksPageData struct {
	Tasks      []*storage.Task
	AgentID    string
	AgentNames map[uuid.UUID]string
}

type NotificationEventsPageData struct {
	Events           []*storage.NotificationEvent
	TaskDescriptions map[uuid.UUID]string
	AgentNames       map[uuid.UUID]string
}

type NotificationEventDetailPageData struct {
	Event         *storage.NotificationEvent
	Deliveries    []*storage.NotificationDelivery
	PayloadPretty string
}

type AccessRoleOption struct {
	Name        string
	Description string
	Group       string
}

type AccessPermissionOption struct {
	Key         string
	Label       string
	Description string
}

type AccessUserCard struct {
	User          *storage.User
	ActionRoles   []string
	AgentRoles    []string
	OtherRoles    []string
	SelectedRoles map[string]bool
}

type AccessPageData struct {
	Users            []*AccessUserCard
	GlobalRoles      []AccessRoleOption
	AgentPermissions []AccessPermissionOption
	Agents           []*storage.Agent
	Flash            string
}

// AgentResponse defines the structure for a single agent in the JSON API response.
type AgentResponse struct {
	ID            string     `json:"id"`
	Hostname      string     `json:"hostname"`
	OS            string     `json:"os"`
	Arch          string     `json:"arch"`
	Status        string     `json:"status"`
	LastHeartbeat *time.Time `json:"last_heartbeat"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Handlers struct {
	logger    *slog.Logger
	storage   *storage.Storage
	templates *template.Template
	notifier  *notifier.MultiNotifier
	broker    *events.EventBroker
}

func NewHandlers(logger *slog.Logger, storage *storage.Storage, templates *template.Template, notifier *notifier.MultiNotifier, broker *events.EventBroker) *Handlers {
	return &Handlers{
		logger:    logger,
		storage:   storage,
		templates: templates,
		notifier:  notifier,
		broker:    broker,
	}
}

func (h *Handlers) sseHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	clientChan := make(chan []byte, 1)
	h.broker.Subscribe(clientChan)
	defer h.broker.Unsubscribe(clientChan)

	h.logger.Info("SSE client connected")

	ctx := r.Context()
	go func() {
		<-ctx.Done()
		h.logger.Info("SSE client disconnected")
		h.broker.Unsubscribe(clientChan)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-clientChan:
			if !ok {
				return
			}
			if !h.canStreamEventToPrincipal(r.Context(), event) {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", event)
			flusher.Flush()
		}
	}
}

func (h *Handlers) home(w http.ResponseWriter, r *http.Request) {
	err := h.templates.ExecuteTemplate(w, "agents.html", nil)
	if err != nil {
		h.logger.Error("Failed to execute template", "error", err)
	}
}

func (h *Handlers) logout(w http.ResponseWriter, r *http.Request) {
	auth.LogoutChallenge(w)
}

func (h *Handlers) listAgents(w http.ResponseWriter, r *http.Request) {
	h.logger.Info("Serving agent list page")
	agents, err := h.storage.Agent.ListAgents(r.Context())
	if err != nil {
		h.logger.Error("Failed to list agents", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	agents = h.filterVisibleAgents(r.Context(), agents)
	err = h.templates.ExecuteTemplate(w, "agents.html", agents)
	if err != nil {
		h.logger.Error("Failed to execute agents template", "error", err)
	}
}

func (h *Handlers) listTasks(w http.ResponseWriter, r *http.Request) {
	agentIDStr := r.URL.Query().Get("agent_id")
	h.logger.Info("Serving task list page", "agent_id", agentIDStr)
	var tasks []*storage.Task
	var err error
	if agentIDStr != "" {
		agentID, err_parse := uuid.Parse(agentIDStr)
		if err_parse != nil {
			http.Error(w, "Invalid Agent ID", http.StatusBadRequest)
			return
		}
		h.logger.Info("Fetching tasks for specific agent", "agent_id", agentID)
		if !h.canViewTasks(r.Context(), agentID) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		tasks, err = h.storage.Task.ListTasksByAgentID(r.Context(), agentID)
	} else {
		h.logger.Info("Fetching all tasks")
		tasks, err = h.storage.Task.ListTasks(r.Context())
	}
	tasks = h.filterTasksForPrincipal(r.Context(), tasks)

	if err != nil {
		h.logger.Error("Failed to list tasks", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	agents, err := h.storage.Agent.ListAgents(r.Context())
	if err != nil {
		h.logger.Error("Failed to list agents for task page", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	agents = h.filterVisibleAgents(r.Context(), agents)
	agentNames := make(map[uuid.UUID]string, len(agents))
	for _, agent := range agents {
		agentNames[agent.ID] = agent.Hostname
	}

	h.logger.Info("Found tasks", "count", len(tasks))
	pageData := TasksPageData{
		Tasks:      tasks,
		AgentID:    agentIDStr,
		AgentNames: agentNames,
	}
	err = h.templates.ExecuteTemplate(w, "tasks.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute tasks template", "error", err)
	}
}

func (h *Handlers) viewTask(w http.ResponseWriter, r *http.Request) {
	taskIDStr := r.URL.Query().Get("id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		http.Error(w, "Invalid Task ID", http.StatusBadRequest)
		return
	}
	task, err := h.storage.Task.GetTaskByID(r.Context(), taskID)
	if err != nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	if !h.canViewTasks(r.Context(), task.AgentID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	result, _ := h.storage.Task.GetTaskResultByTaskID(r.Context(), taskID)
	logs, _ := h.storage.Log.GetLogsByTaskID(r.Context(), taskID)
	var notificationEvents []*storage.NotificationEvent
	if h.storage.Notification != nil {
		notificationEvents, _ = h.storage.Notification.ListNotificationEventsByTaskID(r.Context(), taskID)
	}
	pageData := TaskDetailPageData{Task: task, Result: result, Logs: logs, NotificationEvents: notificationEvents}
	err = h.templates.ExecuteTemplate(w, "task.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute task template", "error", err)
	}
}

func (h *Handlers) listNotificationEvents(w http.ResponseWriter, r *http.Request) {
	if h.storage.Notification == nil {
		http.Error(w, "Notification storage is not configured", http.StatusNotImplemented)
		return
	}

	events, err := h.storage.Notification.ListNotificationEvents(r.Context(), 100)
	if err != nil {
		h.logger.Error("Failed to list notification events", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	events = h.filterNotificationEventsForPrincipal(r.Context(), events)
	agents, err := h.storage.Agent.ListAgents(r.Context())
	if err != nil {
		h.logger.Error("Failed to list agents for notification page", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	agents = h.filterVisibleAgents(r.Context(), agents)
	agentNames := make(map[uuid.UUID]string, len(agents))
	for _, agent := range agents {
		agentNames[agent.ID] = agent.Hostname
	}

	taskDescriptions := make(map[uuid.UUID]string, len(events))
	for _, event := range events {
		task, taskErr := h.storage.Task.GetTaskByID(r.Context(), event.TaskID)
		if taskErr != nil {
			continue
		}
		if task.Description.Valid && task.Description.String != "" {
			taskDescriptions[event.TaskID] = task.Description.String
			continue
		}
		taskDescriptions[event.TaskID] = string(task.TaskType)
	}

	pageData := NotificationEventsPageData{
		Events:           events,
		TaskDescriptions: taskDescriptions,
		AgentNames:       agentNames,
	}
	err = h.templates.ExecuteTemplate(w, "notifications.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute notifications template", "error", err)
	}
}

func (h *Handlers) viewNotificationEvent(w http.ResponseWriter, r *http.Request) {
	if h.storage.Notification == nil {
		http.Error(w, "Notification storage is not configured", http.StatusNotImplemented)
		return
	}

	eventID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid notification event ID", http.StatusBadRequest)
		return
	}

	event, err := h.storage.Notification.GetNotificationEventByID(r.Context(), eventID)
	if err != nil {
		http.Error(w, "Notification event not found", http.StatusNotFound)
		return
	}
	if !h.canViewNotifications(r.Context(), event.AgentID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	deliveries, err := h.storage.Notification.ListNotificationDeliveriesByEventID(r.Context(), eventID)
	if err != nil {
		h.logger.Error("Failed to list notification deliveries", "event_id", eventID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	payloadPretty := string(event.PayloadJSON)
	if len(event.PayloadJSON) > 0 {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, event.PayloadJSON, "", "  "); err == nil {
			payloadPretty = pretty.String()
		}
	}

	pageData := NotificationEventDetailPageData{
		Event:         event,
		Deliveries:    deliveries,
		PayloadPretty: payloadPretty,
	}
	err = h.templates.ExecuteTemplate(w, "notification_event.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute notification_event template", "error", err)
	}
}

func (h *Handlers) retryNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	if h.storage.Notification == nil {
		http.Error(w, "Notification storage is not configured", http.StatusNotImplemented)
		return
	}

	deliveryID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "Invalid notification delivery ID", http.StatusBadRequest)
		return
	}

	delivery, err := h.storage.Notification.GetNotificationDeliveryByID(r.Context(), deliveryID)
	if err != nil {
		http.Error(w, "Notification delivery not found", http.StatusNotFound)
		return
	}
	if delivery.Status != storage.NotificationDeliveryStatusDeadLetter &&
		delivery.Status != storage.NotificationDeliveryStatusFailed &&
		delivery.Status != storage.NotificationDeliveryStatusCancelled {
		http.Error(w, "Notification delivery is not eligible for manual retry", http.StatusBadRequest)
		return
	}
	if auth.PrincipalFromContext(r.Context()) != nil {
		event, err := h.storage.Notification.GetNotificationEventByID(r.Context(), delivery.NotificationEventID)
		if err != nil {
			http.Error(w, "Notification event not found", http.StatusNotFound)
			return
		}
		if !h.canViewNotifications(r.Context(), event.AgentID) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	maxAttempts := delivery.MaxAttempts
	if delivery.Attempt >= maxAttempts {
		maxAttempts = delivery.Attempt + 1
	}
	if err := h.storage.Notification.ScheduleNotificationDeliveryRetry(r.Context(), delivery.ID, maxAttempts, time.Now()); err != nil {
		h.logger.Error("Failed to schedule manual notification retry", "delivery_id", delivery.ID, "error", err)
		http.Error(w, "Failed to schedule retry", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/notifications/"+delivery.NotificationEventID.String(), http.StatusSeeOther)
}

func (h *Handlers) handleNewTask(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.createTask(w, r)
		return
	}
	h.showNewTaskForm(w, r, nil)
}

func (h *Handlers) handleRescheduleTask(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.updateTaskSchedule(w, r)
		return
	}
	h.showRescheduleTaskForm(w, r)
}

func (h *Handlers) accessPage(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	if err := h.storage.User.EnsureRoles(r.Context(), defaultManagedRoles()); err != nil {
		h.logger.Error("Failed to ensure managed roles", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	users, err := h.storage.User.ListUsers(r.Context())
	if err != nil {
		h.logger.Error("Failed to list users", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	agents, err := h.storage.Agent.ListAgents(r.Context())
	if err != nil {
		h.logger.Error("Failed to list agents for access page", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	pageData := AccessPageData{
		Users:            buildAccessUserCards(users),
		GlobalRoles:      buildGlobalRoleOptions(defaultManagedRoles()),
		AgentPermissions: accessPermissionOptions(),
		Agents:           agents,
		Flash:            r.URL.Query().Get("flash"),
	}

	if err := h.templates.ExecuteTemplate(w, "access.html", pageData); err != nil {
		h.logger.Error("Failed to execute access template", "error", err)
	}
}

func (h *Handlers) createUser(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" {
		http.Redirect(w, r, "/access?flash=Username+and+password+are+required", http.StatusSeeOther)
		return
	}

	if err := h.storage.User.CreateUser(r.Context(), username, password); err != nil {
		h.logger.Error("Failed to create user", "username", username, "error", err)
		http.Redirect(w, r, "/access?flash=Failed+to+create+user", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/access?flash=User+created", http.StatusSeeOther)
}

func (h *Handlers) updateUserPassword(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}

	username := r.PathValue("username")
	password := r.FormValue("password")
	if strings.TrimSpace(password) == "" {
		http.Redirect(w, r, "/access?flash=Password+cannot+be+empty", http.StatusSeeOther)
		return
	}

	if err := h.storage.User.UpdateUserPassword(r.Context(), username, password); err != nil {
		h.logger.Error("Failed to update user password", "username", username, "error", err)
		http.Redirect(w, r, "/access?flash=Failed+to+update+password", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/access?flash=Password+updated", http.StatusSeeOther)
}

func (h *Handlers) updateUserStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}

	username := r.PathValue("username")
	isActive := r.FormValue("is_active") == "true"
	if !isActive {
		users, err := h.storage.User.ListUsers(r.Context())
		if err != nil {
			h.logger.Error("Failed to load users for admin status validation", "error", err)
			http.Redirect(w, r, "/access?flash=Failed+to+validate+status+change", http.StatusSeeOther)
			return
		}
		if isLastActiveAdmin(users, username) {
			http.Redirect(w, r, "/access?flash=Cannot+disable+the+last+active+administrator", http.StatusSeeOther)
			return
		}
	}

	if err := h.storage.User.UpdateUserStatus(r.Context(), username, isActive); err != nil {
		h.logger.Error("Failed to update user status", "username", username, "error", err)
		http.Redirect(w, r, "/access?flash=Failed+to+update+status", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/access?flash=Status+updated", http.StatusSeeOther)
}

func (h *Handlers) updateUserRoles(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}

	username := r.PathValue("username")
	roleNames := uniqueRoleNames(r.Form["roles"])
	if !containsAnyRole(roleNames, auth.RoleAdmin, auth.RoleFullAccess) {
		users, err := h.storage.User.ListUsers(r.Context())
		if err != nil {
			h.logger.Error("Failed to load users for admin role validation", "error", err)
			http.Redirect(w, r, "/access?flash=Failed+to+validate+role+change", http.StatusSeeOther)
			return
		}
		if isLastActiveAdmin(users, username) {
			http.Redirect(w, r, "/access?flash=Cannot+remove+admin+from+the+last+active+administrator", http.StatusSeeOther)
			return
		}
	}

	rolesToEnsure := make([]*storage.Role, 0, len(roleNames))
	rolesToEnsure = append(rolesToEnsure, defaultManagedRoles()...)
	for _, roleName := range roleNames {
		if strings.HasPrefix(roleName, auth.AgentRolePrefix) {
			rolesToEnsure = append(rolesToEnsure, &storage.Role{
				Name:        roleName,
				Description: "Access to specific agent",
			})
		}
	}
	if err := h.storage.User.EnsureRoles(r.Context(), rolesToEnsure); err != nil {
		h.logger.Error("Failed to ensure roles", "username", username, "error", err)
		http.Redirect(w, r, "/access?flash=Failed+to+prepare+roles", http.StatusSeeOther)
		return
	}

	if err := h.storage.User.SetUserRoles(r.Context(), username, roleNames); err != nil {
		h.logger.Error("Failed to update user roles", "username", username, "error", err)
		http.Redirect(w, r, "/access?flash=Failed+to+update+roles", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/access?flash=Roles+updated", http.StatusSeeOther)
}

func (h *Handlers) showRescheduleTaskForm(w http.ResponseWriter, r *http.Request) {
	taskIDStr := r.PathValue("id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		http.Error(w, "Invalid Task ID", http.StatusBadRequest)
		return
	}
	task, err := h.storage.Task.GetTaskByID(r.Context(), taskID)
	if err != nil {
		http.Error(w, "Task not found", http.StatusNotFound)
		return
	}
	if !h.canRescheduleTasks(r.Context(), task.AgentID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	pageData := map[string]interface{}{
		"Task":  task,
		"Error": nil,
	}
	err = h.templates.ExecuteTemplate(w, "reschedule_task.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute reschedule_task template", "error", err)
	}
}

func (h *Handlers) updateTaskSchedule(w http.ResponseWriter, r *http.Request) {
	taskIDStr := r.PathValue("id")
	taskID, err := uuid.Parse(taskIDStr)
	if err != nil {
		http.Error(w, "Invalid Task ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}

	scheduleType := r.FormValue("schedule_type")
	var scheduledAt sql.NullTime
	var cronExpression sql.NullString
	var prerequisiteTaskID uuid.NullUUID

	switch scheduleType {
	case "ONCE":
		// The browser sends datetime-local format "YYYY-MM-DDTHH:MM"
		t, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("scheduled_at"), time.Local)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid scheduled_at time format: %v", err), http.StatusBadRequest)
			return
		}
		scheduledAt = sql.NullTime{Time: t.UTC(), Valid: true}
	case "RECURRING":
		cronExpr := r.FormValue("cron_expression")
		if cronExpr == "" {
			http.Error(w, "Cron expression is required for recurring tasks", http.StatusBadRequest)
			return
		}
		cronExpression = sql.NullString{String: cronExpr, Valid: true}
	case "IMMEDIATE":
		// Nothing to set, will be null in DB
	case "CHAINED":
		http.Error(w, "Changing prerequisite task is not supported", http.StatusBadRequest)
		return
	}

	err = h.storage.Task.UpdateTaskSchedule(r.Context(), taskID, sql.NullString{String: scheduleType, Valid: scheduleType != "IMMEDIATE" && scheduleType != ""}, scheduledAt, cronExpression, prerequisiteTaskID)
	if err != nil {
		h.logger.Error("Failed to update task schedule", "error", err)
		http.Error(w, "Failed to update schedule", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/task/view?id="+taskIDStr, http.StatusSeeOther)
}

func (h *Handlers) showNewTaskForm(w http.ResponseWriter, r *http.Request, formError error) {
	agents, err := h.storage.Agent.ListAgents(r.Context())
	if err != nil {
		h.logger.Error("Failed to list agents", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	agents = h.filterAgentsForTaskCreation(r.Context(), agents)

	tasks, err := h.storage.Task.ListTasks(r.Context())
	if err != nil {
		h.logger.Error("Failed to list tasks", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	tasks = h.filterTasksForPrincipal(r.Context(), tasks)

	selectableTasks := make([]struct {
		ID          string
		Description string
	}, len(tasks))

	for i, task := range tasks {
		var description string
		switch task.TaskType {
		case storage.TaskTypeExecCommand:
			description = fmt.Sprintf("CMD: %s", task.Command.String)
		case storage.TaskTypeExecPythonScript:
			description = fmt.Sprintf("Python: %s", task.EntrypointScript.String)
		case storage.TaskTypeFetchFile:
			description = fmt.Sprintf("Fetch: %s", task.SourcePath.String)
		case storage.TaskTypePushFile:
			description = fmt.Sprintf("Push: %s", task.DestinationPath.String)
		case storage.TaskTypeAgentUpdate:
			description = "Agent Update"
		default:
			description = "Unknown Task Type"
		}

		selectableTasks[i] = struct {
			ID          string
			Description string
		}{
			ID:          task.ID.String(),
			Description: fmt.Sprintf("%s... (%s)", task.ID.String()[:8], description),
		}
	}

	pageData := NewTaskPageData{Agents: agents, SelectableTasks: selectableTasks}
	if formError != nil {
		pageData.Error = formError.Error()
	}
	err = h.templates.ExecuteTemplate(w, "new_task.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute new_task template", "error", err)
	}
}

func (h *Handlers) createTask(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10 MB max memory
		h.logger.Error("Failed to parse multipart form", "error", err)
		http.Error(w, "Invalid form submission", http.StatusBadRequest)
		return
	}

	agentIDs, ok := r.Form["agent_id"]
	if !ok || len(agentIDs) == 0 {
		h.showNewTaskForm(w, r, fmt.Errorf("at least one agent ID is required"))
		return
	}

	taskType := storage.TaskType(r.FormValue("task_type"))
	if taskType == "" {
		h.showNewTaskForm(w, r, fmt.Errorf("task type is required"))
		return
	}
	if !h.canRunTaskType(r.Context(), taskType) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	timeout, _ := strconv.Atoi(r.FormValue("timeout_seconds"))
	files := r.MultipartForm.File["files"]

	// --- Handle Scheduling ---
	scheduleType := r.FormValue("schedule_type")
	var scheduledAt sql.NullTime
	var cronExpression sql.NullString
	var prerequisiteTaskID uuid.NullUUID

	switch scheduleType {
	case "ONCE":
		t, err := time.ParseInLocation("2006-01-02 15:04", r.FormValue("scheduled_at"), time.Local)
		if err != nil {
			h.showNewTaskForm(w, r, fmt.Errorf("invalid scheduled_at time format: %w", err))
			return
		}
		scheduledAt = sql.NullTime{Time: t.UTC(), Valid: true}
	case "RECURRING":
		cronExpr := r.FormValue("cron_expression")
		if cronExpr == "" { // Basic validation
			h.showNewTaskForm(w, r, fmt.Errorf("cron expression is required for recurring tasks"))
			return
		}
		cronExpression = sql.NullString{String: cronExpr, Valid: true}
	case "CHAINED":
		id, err := uuid.Parse(r.FormValue("prerequisite_task_id"))
		if err != nil {
			h.showNewTaskForm(w, r, fmt.Errorf("invalid prerequisite task ID: %w", err))
			return
		}
		prerequisiteTaskID = uuid.NullUUID{UUID: id, Valid: true}
	}

	for _, agentIDStr := range agentIDs {
		agentID, err := uuid.Parse(agentIDStr)
		if err != nil {
			h.showNewTaskForm(w, r, fmt.Errorf("invalid agent ID: %s", agentIDStr))
			return
		}
		if !h.canCreateTasks(r.Context(), agentID) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		task := storage.Task{
			ID:                  uuid.New(),
			AgentID:             agentID,
			Description:         sql.NullString{String: r.FormValue("description"), Valid: r.FormValue("description") != ""},
			TaskType:            taskType,
			ResultContract:      sql.NullString{String: r.FormValue("result_contract"), Valid: r.FormValue("result_contract") != ""},
			NotificationRuleSet: sql.NullString{String: r.FormValue("notification_rule_set"), Valid: r.FormValue("notification_rule_set") != ""},
			Status:              storage.TaskStatusPending,
			TimeoutSeconds:      sql.NullInt32{Int32: int32(timeout), Valid: true},
			ScheduleType:        sql.NullString{String: scheduleType, Valid: scheduleType != "" && scheduleType != "IMMEDIATE"},
			ScheduledAt:         scheduledAt,
			CronExpression:      cronExpression,
			PrerequisiteTaskID:  prerequisiteTaskID,
			CreatedAt:           time.Now(),
			UpdatedAt:           time.Now(),
			CreatedBy:           h.createdByPrincipal(r.Context()),
		}
		rawDestinations := strings.Split(strings.TrimSpace(r.FormValue("default_destinations")), ",")
		defaultDestinations := make([]string, 0, len(rawDestinations))
		for _, destination := range rawDestinations {
			trimmed := strings.TrimSpace(destination)
			if trimmed == "" {
				continue
			}
			defaultDestinations = append(defaultDestinations, trimmed)
		}
		if len(defaultDestinations) > 0 {
			task.DefaultDestinations = pq.StringArray(defaultDestinations)
		}

		// --- Handle File Uploads ---
		var savedFilePaths []string
		if len(files) > 0 {
			taskUploadDir := filepath.Join("./uploads", task.ID.String())
			if err := os.MkdirAll(taskUploadDir, os.ModePerm); err != nil {
				h.logger.Error("Failed to create task upload directory", "error", err, "dir", taskUploadDir)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			for _, fileHeader := range files {
				src, err := fileHeader.Open()
				if err != nil {
					h.logger.Error("Failed to open uploaded file", "error", err)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				}
				defer src.Close()

				dstPath := filepath.Join(taskUploadDir, fileHeader.Filename)
				dst, err := os.Create(dstPath)
				if err != nil {
					h.logger.Error("Failed to create destination file", "error", err, "path", dstPath)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				}
				defer dst.Close()

				if _, err := io.Copy(dst, src); err != nil {
					h.logger.Error("Failed to copy uploaded file content", "error", err)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				}
				savedFilePaths = append(savedFilePaths, dstPath)
				h.logger.Info("Successfully saved uploaded file", "path", dstPath)
			}
		}

		switch taskType {
		case storage.TaskTypeExecCommand:
			task.Command = sql.NullString{String: r.FormValue("command"), Valid: r.FormValue("command") != ""}
			args := r.FormValue("args")
			if args != "" {
				task.Args = pq.StringArray(strings.Split(args, ","))
			}
		case storage.TaskTypeExecPythonScript:
			if len(savedFilePaths) > 0 {
				task.PackageFiles = pq.StringArray(savedFilePaths)
			}
			task.EntrypointScript = sql.NullString{String: r.FormValue("entrypoint_script"), Valid: r.FormValue("entrypoint_script") != ""}
		case storage.TaskTypePushFile:
			if len(savedFilePaths) > 0 {
				task.PackageFiles = pq.StringArray(savedFilePaths)
			}
			task.DestinationPath = sql.NullString{String: r.FormValue("destination_path_push"), Valid: r.FormValue("destination_path_push") != ""}
		case storage.TaskTypeFetchFile:
			task.SourcePath = sql.NullString{String: r.FormValue("source_path_fetch"), Valid: r.FormValue("source_path_fetch") != ""}
			task.DestinationPath = sql.NullString{String: r.FormValue("destination_path_fetch"), Valid: r.FormValue("destination_path_fetch") != ""}
		case storage.TaskTypeAgentUpdate:
			if len(savedFilePaths) == 1 {
				task.PackageFiles = pq.StringArray(savedFilePaths)
				task.SourcePath = sql.NullString{String: savedFilePaths[0], Valid: true}
			}
			task.Command = sql.NullString{String: r.FormValue("checksum"), Valid: r.FormValue("checksum") != ""}
		}

		if err := h.storage.Task.CreateTask(r.Context(), &task); err != nil {
			h.logger.Error("Failed to save task for agent", "agent_id", agentID, "error", err)
			http.Error(w, "Failed to save task", http.StatusInternalServerError)
			return
		}
		h.logger.Info("Successfully created task for agent", "task_id", task.ID, "agent_id", agentID)
	}

	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}
func (h *Handlers) AgentMetricsPage(w http.ResponseWriter, r *http.Request) {
	agentIDStr := r.PathValue("id")
	agentID, err := uuid.Parse(agentIDStr)
	if err != nil {
		http.Error(w, "Invalid Agent ID", http.StatusBadRequest)
		return
	}
	agent, err := h.storage.Agent.GetAgentByID(r.Context(), agentID)
	if err != nil {
		http.Error(w, "Agent Not Found", http.StatusNotFound)
		return
	}
	if !h.canViewMetrics(r.Context(), agent.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	since := time.Now().Add(-6 * time.Hour)
	metrics, _ := h.storage.Metric.GetMetricsByAgentID(r.Context(), agentID, since)
	chartData, _ := formatDataForChartJS(metrics)
	pageData := AgentMetricsPageData{Agent: agent, ChartJSData: template.JS(chartData)}
	err = h.templates.ExecuteTemplate(w, "metrics.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute metrics template", "error", err)
	}
}

func (h *Handlers) toggleAgentStatus(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	idStr := r.FormValue("id")
	agentID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}
	agent, err := h.storage.Agent.GetAgentByID(r.Context(), agentID)
	if err != nil {
		http.Error(w, "Agent not found", http.StatusNotFound)
		return
	}
	if !h.canToggleAgentStatus(r.Context(), agent.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	newStatus := storage.AgentStatusDisconnected
	if agent.Status != storage.AgentStatusOnline {
		newStatus = storage.AgentStatusOnline
	}
	h.storage.Agent.UpdateAgentStatus(r.Context(), agentID, newStatus, time.Now())
	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

func (h *Handlers) deleteAgent(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	idStr := r.FormValue("id")
	agentID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "Invalid agent ID", http.StatusBadRequest)
		return
	}
	if !h.canDeleteAgent(r.Context(), agentID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	h.storage.Agent.DeleteAgent(r.Context(), agentID)
	http.Redirect(w, r, "/agents", http.StatusSeeOther)
}

func (h *Handlers) listAgentsJSON(w http.ResponseWriter, r *http.Request) {
	agents, err := h.storage.Agent.ListAgents(r.Context())
	if err != nil {
		h.logger.Error("Failed to list agents for JSON API", "error", err)
		http.Error(w, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
		return
	}

	agents = h.filterVisibleAgents(r.Context(), agents)
	responses := make([]AgentResponse, 0, len(agents))
	for _, agent := range agents {
		var lastHeartbeat *time.Time
		if agent.LastHeartbeat.Valid {
			lastHeartbeat = &agent.LastHeartbeat.Time
		}
		responses = append(responses, AgentResponse{
			ID:            agent.ID.String(),
			Hostname:      agent.Hostname,
			OS:            agent.OS.String,
			Arch:          agent.Arch.String,
			Status:        string(agent.Status),
			LastHeartbeat: lastHeartbeat,
			CreatedAt:     agent.CreatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(responses); err != nil {
		h.logger.Error("Failed to encode agents to JSON", "error", err)
		// The response header might already be written, so we can't send a 500.
		// The connection will likely be closed by the server.
	}
}

func buildAccessUserCards(users []*storage.User) []*AccessUserCard {
	result := make([]*AccessUserCard, 0, len(users))
	for _, user := range users {
		if user == nil {
			continue
		}

		card := &AccessUserCard{
			User:          user,
			SelectedRoles: make(map[string]bool, len(user.Roles)),
		}
		for _, role := range user.Roles {
			card.SelectedRoles[role] = true
			switch {
			case role == auth.RoleAdmin, role == auth.RoleFullAccess:
				card.OtherRoles = append(card.OtherRoles, role)
			case strings.HasPrefix(role, auth.ActionRolePrefix):
				card.ActionRoles = append(card.ActionRoles, role)
			case strings.HasPrefix(role, auth.AgentRolePrefix):
				card.AgentRoles = append(card.AgentRoles, role)
			default:
				card.OtherRoles = append(card.OtherRoles, role)
			}
		}
		sort.Strings(card.ActionRoles)
		sort.Strings(card.AgentRoles)
		sort.Strings(card.OtherRoles)
		result = append(result, card)
	}

	return result
}

func buildGlobalRoleOptions(roles []*storage.Role) []AccessRoleOption {
	options := make([]AccessRoleOption, 0, len(roles))
	for _, role := range roles {
		if role == nil || strings.HasPrefix(role.Name, auth.AgentRolePrefix) {
			continue
		}
		options = append(options, AccessRoleOption{
			Name:        role.Name,
			Description: role.Description,
			Group:       classifyRoleGroup(role.Name),
		})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Group == options[j].Group {
			return options[i].Name < options[j].Name
		}
		return options[i].Group < options[j].Group
	})
	return options
}

func accessPermissionOptions() []AccessPermissionOption {
	return []AccessPermissionOption{
		{Key: auth.AgentPermissionView, Label: "View", Description: "See the agent on the main page"},
		{Key: auth.AgentPermissionMetricsView, Label: "Metrics", Description: "Open metrics for the agent"},
		{Key: auth.AgentPermissionTaskView, Label: "Tasks", Description: "View tasks and task details"},
		{Key: auth.AgentPermissionTaskCreate, Label: "Create", Description: "Create tasks for the agent"},
		{Key: auth.AgentPermissionTaskReschedule, Label: "Reschedule", Description: "Change schedule of existing tasks"},
		{Key: auth.AgentPermissionNotificationView, Label: "Alerts", Description: "View notifications and retries"},
		{Key: auth.AgentPermissionStatusToggle, Label: "Toggle", Description: "Toggle agent status"},
		{Key: auth.AgentPermissionDelete, Label: "Delete", Description: "Delete the agent"},
	}
}

func classifyRoleGroup(roleName string) string {
	switch {
	case roleName == auth.RoleAdmin, roleName == auth.RoleFullAccess:
		return "Core"
	case strings.HasPrefix(roleName, auth.ActionRolePrefix):
		return "Actions"
	case strings.HasPrefix(roleName, auth.AgentRolePrefix):
		return "Agents"
	default:
		return "Other"
	}
}

func uniqueRoleNames(roleNames []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(roleNames))
	for _, roleName := range roleNames {
		roleName = strings.TrimSpace(strings.ToLower(roleName))
		if roleName == "" || seen[roleName] {
			continue
		}
		seen[roleName] = true
		result = append(result, roleName)
	}
	sort.Strings(result)
	return result
}

func containsAnyRole(roleNames []string, expected ...string) bool {
	for _, roleName := range roleNames {
		for _, candidate := range expected {
			if roleName == candidate {
				return true
			}
		}
	}
	return false
}

func isLastActiveAdmin(users []*storage.User, username string) bool {
	activeAdmins := 0
	targetIsActiveAdmin := false
	for _, user := range users {
		if user == nil || !user.IsActive {
			continue
		}
		isAdmin := false
		for _, role := range user.Roles {
			if role == auth.RoleAdmin || role == auth.RoleFullAccess {
				isAdmin = true
				break
			}
		}
		if !isAdmin {
			continue
		}
		activeAdmins++
		if user.Username == username {
			targetIsActiveAdmin = true
		}
	}
	return targetIsActiveAdmin && activeAdmins <= 1
}

func defaultManagedRoles() []*storage.Role {
	return []*storage.Role{
		{Name: auth.RoleAdmin, Description: "Full access to all agents and actions"},
		{Name: auth.RoleFullAccess, Description: "Full access to all agents and actions without the admin label"},
		{Name: "action.exec_command", Description: "Allows creating and managing EXEC_COMMAND tasks"},
		{Name: "action.exec_python_script", Description: "Allows creating and managing EXEC_PYTHON_SCRIPT tasks"},
		{Name: "action.fetch_file", Description: "Allows creating and managing FETCH_FILE tasks"},
		{Name: "action.push_file", Description: "Allows creating and managing PUSH_FILE tasks"},
		{Name: "action.agent_update", Description: "Allows creating and managing AGENT_UPDATE tasks"},
	}
}

func (h *Handlers) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil || !principal.IsAdmin() {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (h *Handlers) createdByPrincipal(ctx context.Context) sql.NullString {
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.Username == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: principal.Username, Valid: true}
}

func (h *Handlers) canSeeAgent(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAnyAgentPermission(agentID)
}

func (h *Handlers) canRunTaskType(ctx context.Context, taskType storage.TaskType) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.CanRunTaskType(taskType)
}

func (h *Handlers) canViewMetrics(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAgentPermission(agentID, auth.AgentPermissionMetricsView)
}

func (h *Handlers) canViewTasks(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAgentPermission(agentID, auth.AgentPermissionTaskView)
}

func (h *Handlers) canCreateTasks(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAgentPermission(agentID, auth.AgentPermissionTaskCreate)
}

func (h *Handlers) canRescheduleTasks(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAgentPermission(agentID, auth.AgentPermissionTaskReschedule)
}

func (h *Handlers) canViewNotifications(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAgentPermission(agentID, auth.AgentPermissionNotificationView)
}

func (h *Handlers) canToggleAgentStatus(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAgentPermission(agentID, auth.AgentPermissionStatusToggle)
}

func (h *Handlers) canDeleteAgent(ctx context.Context, agentID uuid.UUID) bool {
	principal := auth.PrincipalFromContext(ctx)
	return principal == nil || principal.HasAgentPermission(agentID, auth.AgentPermissionDelete)
}

func (h *Handlers) filterVisibleAgents(ctx context.Context, agents []*storage.Agent) []*storage.Agent {
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.IsAdmin() {
		return agents
	}

	filtered := make([]*storage.Agent, 0, len(agents))
	for _, agent := range agents {
		if agent != nil && principal.HasAnyAgentPermission(agent.ID) {
			filtered = append(filtered, agent)
		}
	}
	return filtered
}

func (h *Handlers) filterAgentsForTaskCreation(ctx context.Context, agents []*storage.Agent) []*storage.Agent {
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.IsAdmin() {
		return agents
	}

	filtered := make([]*storage.Agent, 0, len(agents))
	for _, agent := range agents {
		if agent != nil && principal.HasAgentPermission(agent.ID, auth.AgentPermissionTaskCreate) {
			filtered = append(filtered, agent)
		}
	}
	return filtered
}

func (h *Handlers) filterTasksForPrincipal(ctx context.Context, tasks []*storage.Task) []*storage.Task {
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.IsAdmin() {
		return tasks
	}

	filtered := make([]*storage.Task, 0, len(tasks))
	for _, task := range tasks {
		if task != nil && principal.HasAgentPermission(task.AgentID, auth.AgentPermissionTaskView) {
			filtered = append(filtered, task)
		}
	}
	return filtered
}

func (h *Handlers) filterNotificationEventsForPrincipal(ctx context.Context, events []*storage.NotificationEvent) []*storage.NotificationEvent {
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.IsAdmin() {
		return events
	}

	filtered := make([]*storage.NotificationEvent, 0, len(events))
	for _, event := range events {
		if event != nil && principal.HasAgentPermission(event.AgentID, auth.AgentPermissionNotificationView) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func (h *Handlers) canStreamEventToPrincipal(ctx context.Context, payload []byte) bool {
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.IsAdmin() {
		return true
	}

	var task storage.Task
	if err := json.Unmarshal(payload, &task); err != nil {
		return false
	}

	return principal.HasAgentPermission(task.AgentID, auth.AgentPermissionTaskView)
}

func formatDataForChartJS(metrics []*storage.AgentMetric) (string, error) {
	labels := make([]string, len(metrics))
	cpuData := make([]float32, len(metrics))
	ramData := make([]float32, len(metrics))
	for i, m := range metrics {
		labels[i] = m.Timestamp.Format("15:04:05")
		cpuData[i] = m.CPUUsage
		ramData[i] = m.RAMUsage
	}
	chartData := map[string]interface{}{
		"labels": labels,
		"datasets": []map[string]interface{}{
			{"label": "CPU Usage (%)", "data": cpuData, "borderColor": "rgb(255, 99, 132)"},
			{"label": "RAM Usage (%)", "data": ramData, "borderColor": "rgb(54, 162, 235)"},
		},
	}
	b, err := json.Marshal(chartData)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
