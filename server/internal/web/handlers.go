package web

import (
	"agent-management/server/internal/events"
	"agent-management/server/internal/notifier"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-management/server/internal/storage"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type TaskDetailPageData struct {
	Task   *storage.Task
	Result *storage.TaskResult
	Logs   []*storage.Log
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

func (h *Handlers) listAgents(w http.ResponseWriter, r *http.Request) {
	h.logger.Info("Serving agent list page")
	agents, err := h.storage.Agent.ListAgents(r.Context())
	if err != nil {
		h.logger.Error("Failed to list agents", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
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
		tasks, err = h.storage.Task.ListTasksByAgentID(r.Context(), agentID)
	} else {
		h.logger.Info("Fetching all tasks")
		tasks, err = h.storage.Task.ListTasks(r.Context())
	}

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
	result, _ := h.storage.Task.GetTaskResultByTaskID(r.Context(), taskID)
	logs, _ := h.storage.Log.GetLogsByTaskID(r.Context(), taskID)
	pageData := TaskDetailPageData{Task: task, Result: result, Logs: logs}
	err = h.templates.ExecuteTemplate(w, "task.html", pageData)
	if err != nil {
		h.logger.Error("Failed to execute task template", "error", err)
	}
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
		t, err := time.Parse("2006-01-02T15:04", r.FormValue("scheduled_at"))
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid scheduled_at time format: %v", err), http.StatusBadRequest)
			return
		}
		scheduledAt = sql.NullTime{Time: t, Valid: true}
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

	tasks, err := h.storage.Task.ListTasks(r.Context())
	if err != nil {
		h.logger.Error("Failed to list tasks", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

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
	timeout, _ := strconv.Atoi(r.FormValue("timeout_seconds"))
	files := r.MultipartForm.File["files"]

	// --- Handle Scheduling ---
	scheduleType := r.FormValue("schedule_type")
	var scheduledAt sql.NullTime
	var cronExpression sql.NullString
	var prerequisiteTaskID uuid.NullUUID

	switch scheduleType {
	case "ONCE":
		t, err := time.Parse("2006-01-02 15:04", r.FormValue("scheduled_at"))
		if err != nil {
			h.showNewTaskForm(w, r, fmt.Errorf("invalid scheduled_at time format: %w", err))
			return
		}
		scheduledAt = sql.NullTime{Time: t, Valid: true}
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

		task := storage.Task{
			ID:                 uuid.New(),
			AgentID:            agentID,
			Description:        sql.NullString{String: r.FormValue("description"), Valid: r.FormValue("description") != ""},
			TaskType:           taskType,
			Status:             storage.TaskStatusPending,
			TimeoutSeconds:     sql.NullInt32{Int32: int32(timeout), Valid: true},
			ScheduleType:       sql.NullString{String: scheduleType, Valid: scheduleType != "" && scheduleType != "IMMEDIATE"},
			ScheduledAt:        scheduledAt,
			CronExpression:     cronExpression,
			PrerequisiteTaskID: prerequisiteTaskID,
			CreatedAt:          time.Now(),
			UpdatedAt:          time.Now(),
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
