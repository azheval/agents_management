package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"agent-management/server/internal"
	"agent-management/server/internal/config"
	"agent-management/server/internal/logger"
	"agent-management/server/internal/notifier"
	"agent-management/server/internal/scheduler"
	"agent-management/server/internal/storage"
	"agent-management/server/internal/web"
	"github.com/google/uuid"
)

func main() {
	// Define and parse the command-line flag for the config file path
	configPath := flag.String("config", "server/config.json", "path to the configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		// Use a default logger for startup errors
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("failed to load configuration", "path", *configPath, "error", err)
		os.Exit(1)
	}

	// Initialize logger
	appLogger := logger.New(cfg.Logging)
	appLogger.Info("Logger initialized successfully")

	// Establish database connection
	db, err := storage.NewDBConnection(storage.DBConfig{
		Host:     cfg.Database.Host,
		Port:     cfg.Database.Port,
		Username: cfg.Database.Username,
		Password: cfg.Database.Password,
		DBName:   cfg.Database.DBName,
		SSLMode:  cfg.Database.SSLMode,
	})
	if err != nil {
		appLogger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	appLogger.Info("Successfully connected to the database")

	// Create storage repositories
	agentRepo := storage.NewPostgresAgentRepository(db)
	taskRepo := storage.NewPostgresTaskRepository(db)
	logRepo := storage.NewPostgresLogRepository(db)
	metricRepo := storage.NewPostgresMetricRepository(db)
	notificationRepo := storage.NewPostgresNotificationRepository(db)
	appStorage := storage.NewStorage(agentRepo, taskRepo, logRepo, metricRepo, notificationRepo)

	// Create and start the agent status service
	statusLogger := appLogger.With("service", "AgentStatusService")
	statusService := internal.NewAgentStatusService(statusLogger, appStorage, cfg.AgentStatusService.CheckInterval, cfg.AgentStatusService.InactiveThreshold)
	go statusService.Start(context.Background())

	// Create notifier
	notifierLogger := appLogger.With("component", "Notifier")
	notifiers := []notifier.Notifier{
		notifier.NewLogNotifier(notifierLogger),
	}
	var telegramNotifier *notifier.TelegramNotifier
	if cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != 0 {
		telegramNotifier, err = notifier.NewTelegramNotifier(cfg.Telegram.BotToken, cfg.Telegram.ChatID, notifierLogger)
		if err != nil {
			appLogger.Error("failed to initialize telegram notifier", "error", err)
		} else {
			notifiers = append(notifiers, telegramNotifier)
			appLogger.Info("Telegram notifier initialized and added")
		}
	}
	multiNotifier := notifier.NewMultiNotifier(notifierLogger, notifiers...)

	notificationRuleEngine := internal.NewNotificationRuleEngine(internal.NewDefaultNotificationPolicy())
	notificationRoutes := make([]internal.NotificationRoute, 0, 1)
	deliveryAdapters := make([]internal.NotificationDeliveryAdapter, 0, 1)
	if telegramNotifier != nil {
		notificationRoutes = append(notificationRoutes, internal.NotificationRoute{
			Channel:     "telegram",
			Destination: fmt.Sprintf("%d", cfg.Telegram.ChatID),
			MaxAttempts: 3,
		})
		deliveryAdapters = append(deliveryAdapters, telegramNotifier)
	}
	notificationRouter := internal.NewNotificationRouter(notificationRoutes...)
	notificationDispatcher := internal.NewNotificationDeliveryDispatcher(notifierLogger, deliveryAdapters...)

	// Create the task queue and scheduler
	taskQueue := make(chan uuid.UUID, 100)
	schedulerLogger := appLogger.With("service", "Scheduler")
	taskScheduler := scheduler.NewScheduler(schedulerLogger, appStorage.Task, taskQueue)
	if err := taskScheduler.Start(); err != nil {
		appLogger.Error("Failed to start task scheduler", "error", err)
		os.Exit(1)
	}
	defer taskScheduler.Stop()

	// Start the Web server
	eventBroker := web.StartWebServer(cfg.Webserver, appLogger, appStorage, multiNotifier)
	defer eventBroker.Stop()

	serverInstance := internal.NewServer(appLogger, appStorage, multiNotifier, taskQueue, eventBroker)
	serverInstance.SetNotificationPipeline(notificationRuleEngine, notificationRouter)
	serverInstance.SetNotificationDeliveryDispatcher(notificationDispatcher)

	retryProcessor := internal.NewNotificationRetryProcessor(
		appLogger.With("service", "NotificationRetryProcessor"),
		appStorage,
		serverInstance,
		30*time.Second,
		50,
	)
	go retryProcessor.Start(context.Background())

	// Start the gRPC server (this will block)
	internal.StartServer(cfg.Server, appLogger, appStorage, multiNotifier, taskQueue, eventBroker, notificationRuleEngine, notificationRouter, notificationDispatcher)
}
