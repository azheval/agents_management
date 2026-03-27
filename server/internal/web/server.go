package web

import (
	"agent-management/server/internal/events"
	"agent-management/server/internal/notifier"
	"html/template"
	"log/slog"
	"net/http"
	"strings" // Import the strings package

	"agent-management/server/internal/config"
	"agent-management/server/internal/storage"
)

// Define the function map.
var funcMap = template.FuncMap{
	"lower": strings.ToLower,
}

// StartWebServer initializes and starts the web server.
func StartWebServer(cfg config.WebserverConfig, logger *slog.Logger, storage *storage.Storage, notifier *notifier.MultiNotifier) *events.EventBroker {
	webLogger := logger.With("component", "webserver")

	// Use template.Must to panic if template parsing fails, and add the custom function.
	templates := template.Must(template.New("").Funcs(funcMap).ParseGlob("server/internal/web/templates/*.html"))

	broker := events.NewEventBroker(webLogger)
	go broker.Start()

	webLogger.Info("Starting web server...", "address", cfg.ListenAddress)

	mux := http.NewServeMux()

	// Serve static files from the "static" directory
	fs := http.FileServer(http.Dir("server/internal/web/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	handlers := NewHandlers(webLogger, storage, templates, notifier, broker)

	// Register routes
	mux.HandleFunc("/", handlers.listAgents)
	mux.HandleFunc("/agents", handlers.listAgents)
	mux.HandleFunc("GET /agents/{id}/metrics", handlers.AgentMetricsPage)
	mux.HandleFunc("/tasks", handlers.listTasks)
	mux.HandleFunc("/task/view", handlers.viewTask)
	mux.HandleFunc("/task/new", handlers.handleNewTask)
	mux.HandleFunc("GET /task/{id}/reschedule", handlers.handleRescheduleTask)
	mux.HandleFunc("POST /task/{id}/reschedule", handlers.handleRescheduleTask)
	mux.HandleFunc("/agent/toggle_status", handlers.toggleAgentStatus)
	mux.HandleFunc("/agent/delete", handlers.deleteAgent)
	mux.HandleFunc("/events", handlers.sseHandler)

	go func() {
		if err := http.ListenAndServe(cfg.ListenAddress, mux); err != nil {
			webLogger.Error("Web server failed", "error", err)
		}
	}()

	return broker
}
