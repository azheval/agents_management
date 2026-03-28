package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"agent-management/server/internal/storage"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TelegramNotifier sends notifications to a Telegram chat.
type TelegramNotifier struct {
	bot    *tgbotapi.BotAPI
	chatID int64
	logger *slog.Logger
}

// NewTelegramNotifier creates and returns a new TelegramNotifier.
// It requires a bot token and a chat ID.
func NewTelegramNotifier(token string, chatID int64, logger *slog.Logger) (*TelegramNotifier, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	logger.Info("Telegram bot authorized", "account", bot.Self.UserName)

	return &TelegramNotifier{
		bot:    bot,
		chatID: chatID,
		logger: logger,
	}, nil
}

func (tn *TelegramNotifier) Channel() string {
	return "telegram"
}

// NotifyTaskCompletion sends a message to Telegram about task completion.
func (tn *TelegramNotifier) NotifyTaskCompletion(task *storage.Task, result *storage.TaskResult) error {
	statusIcon := "✅" // Success
	if result.Status != storage.TaskResultStatusSuccess {
		statusIcon = "❌" // Failure
	}

	// Using Markdown for formatting.
	messageText := fmt.Sprintf(
		`%s *Task Completed*
`+"```"+`
Task ID:   %s
Agent ID:  %s
Type:      %s
Command:   %s
Status:    %s
Exit Code: %d
Duration:  %dms
`+"```",
		statusIcon,
		task.ID,
		task.AgentID,
		task.TaskType,
		task.Command.String,
		result.Status,
		result.ExitCode.Int32,
		result.DurationMs.Int64,
	)

	msg := tgbotapi.NewMessage(tn.chatID, messageText)
	msg.ParseMode = tgbotapi.ModeMarkdown

	if _, err := tn.bot.Send(msg); err != nil {
		tn.logger.Error("Failed to send telegram notification", "task_id", task.ID, "error", err)
		return err
	}

	tn.logger.Info("Sent telegram notification successfully", "task_id", task.ID)
	return nil
}

// NotifyAgentStatusChange sends a message to Telegram about an agent's status change.
func (tn *TelegramNotifier) NotifyAgentStatusChange(agent *storage.Agent, oldStatus, newStatus storage.AgentStatus) error {
	statusIcon := "🟢" // Online
	if newStatus != storage.AgentStatusOnline {
		statusIcon = "🔴" // Offline or Disconnected
	}

	messageText := fmt.Sprintf(
		`%s *Agent Status Change*
`+"```"+`
Agent ID: %s
Hostname: %s
Status:   %s -> %s
`+"```",
		statusIcon,
		agent.ID,
		agent.Hostname,
		oldStatus,
		newStatus,
	)

	msg := tgbotapi.NewMessage(tn.chatID, messageText)
	msg.ParseMode = tgbotapi.ModeMarkdown

	if _, err := tn.bot.Send(msg); err != nil {
		tn.logger.Error("Failed to send agent status telegram notification", "agent_id", agent.ID, "error", err)
		return err
	}

	tn.logger.Info("Sent agent status telegram notification successfully", "agent_id", agent.ID)
	return nil
}

func (tn *TelegramNotifier) DeliverNotificationEvent(ctx context.Context, event *storage.NotificationEvent, delivery *storage.NotificationDelivery) (string, []byte, error) {
	chatID, err := tn.resolveChatID(delivery.Destination)
	if err != nil {
		return "", nil, err
	}

	msg := tgbotapi.NewMessage(chatID, tn.formatNotificationEventMessage(event))
	msg.ParseMode = ""

	sentMessage, err := tn.bot.Send(msg)
	if err != nil {
		tn.logger.Error("Failed to send telegram delivery", "event_id", event.ID, "delivery_id", delivery.ID, "error", err)
		return "", nil, err
	}

	responseJSON, err := json.Marshal(sentMessage)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal telegram response: %w", err)
	}

	return strconv.Itoa(sentMessage.MessageID), responseJSON, nil
}

func (tn *TelegramNotifier) resolveChatID(destination string) (int64, error) {
	trimmed := strings.TrimSpace(destination)
	if trimmed == "" || trimmed == "default" {
		if tn.chatID == 0 {
			return 0, fmt.Errorf("telegram chat id is not configured")
		}
		return tn.chatID, nil
	}

	chatID, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid telegram destination %q: %w", destination, err)
	}
	return chatID, nil
}

func (tn *TelegramNotifier) formatNotificationEventMessage(event *storage.NotificationEvent) string {
	return fmt.Sprintf(
		"[ALERT]\nSeverity: %s\nType: %s\nTitle: %s\nSummary: %s\nTask ID: %s\nAgent ID: %s",
		strings.ToUpper(event.Severity),
		event.EventType,
		event.Title,
		event.Summary,
		event.TaskID,
		event.AgentID,
	)
}
