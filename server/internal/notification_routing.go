package internal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"agent-management/server/internal/storage"

	"github.com/google/uuid"
)

type NotificationRule struct {
	Name       string
	Enabled    bool
	EventTypes []string
	Severities []string
	Labels     map[string]string
	Channels   []string
}

type NotificationPolicy struct {
	DefaultChannels   []string
	DefaultSeverities []string
	Rules             []NotificationRule
}

type NotificationRuleEngine struct {
	policy NotificationPolicy
}

type NotificationRoutingDecision struct {
	Status   storage.NotificationEventStatus
	Reason   string
	Channels []string
}

type NotificationRoute struct {
	Channel     string
	Destination string
	MaxAttempts int32
}

type NotificationRouter struct {
	routes map[string]NotificationRoute
}

func NewNotificationRuleEngine(policy NotificationPolicy) *NotificationRuleEngine {
	return &NotificationRuleEngine{policy: normalizeNotificationPolicy(policy)}
}

func NewDefaultNotificationPolicy() NotificationPolicy {
	return NotificationPolicy{
		DefaultChannels:   []string{"telegram"},
		DefaultSeverities: []string{"warning", "error", "critical"},
	}
}

func NewNotificationRouter(routes ...NotificationRoute) *NotificationRouter {
	router := &NotificationRouter{routes: make(map[string]NotificationRoute, len(routes))}
	for _, route := range routes {
		if route.Channel == "" || route.Destination == "" {
			continue
		}
		if route.MaxAttempts <= 0 {
			route.MaxAttempts = 3
		}
		router.routes[route.Channel] = route
	}
	return router
}

func (e *NotificationRuleEngine) Evaluate(task *storage.Task, event *storage.NotificationEvent) (*NotificationRoutingDecision, error) {
	if event == nil {
		return nil, fmt.Errorf("notification event is nil")
	}

	policy := e.policyForTask(task)
	payload, err := payloadFromEvent(event)
	if err != nil {
		return nil, err
	}
	if !payload.Notify {
		return &NotificationRoutingDecision{
			Status: storage.NotificationEventStatusSuppressed,
			Reason: "payload notify=false",
		}, nil
	}

	channels := e.channelsFromRules(policy, event, payload)
	if len(channels) == 0 && slices.Contains(policy.DefaultSeverities, event.Severity) {
		channels = append(channels, policy.DefaultChannels...)
	}
	channels = uniqueStrings(channels)

	if len(channels) == 0 {
		return &NotificationRoutingDecision{
			Status: storage.NotificationEventStatusSuppressed,
			Reason: "no matching notification rules",
		}, nil
	}

	return &NotificationRoutingDecision{
		Status:   storage.NotificationEventStatusAccepted,
		Reason:   "matched notification policy",
		Channels: channels,
	}, nil
}

func (r *NotificationRouter) Resolve(task *storage.Task, channels []string) ([]NotificationRoute, error) {
	if task != nil && len(task.DefaultDestinations) > 0 {
		return r.resolveTaskDestinations(task.DefaultDestinations)
	}
	if len(channels) == 0 {
		return nil, nil
	}

	resolved := make([]NotificationRoute, 0, len(channels))
	missing := make([]string, 0)
	for _, channel := range uniqueStrings(channels) {
		route, ok := r.routes[channel]
		if !ok {
			missing = append(missing, channel)
			continue
		}
		resolved = append(resolved, route)
	}

	if len(missing) > 0 {
		return resolved, fmt.Errorf("missing routes for channels: %s", strings.Join(missing, ", "))
	}
	return resolved, nil
}

func (s *Server) processNotificationEvent(ctx context.Context, task *storage.Task, event *storage.NotificationEvent) {
	if s.storage == nil || s.storage.Notification == nil || event == nil {
		return
	}
	if s.notificationRuleEngine == nil || s.notificationRouter == nil {
		return
	}
	if s.shouldSuppressDuplicateEvent(ctx, event) {
		s.logger.Info("Notification event suppressed by deduplication", "event_id", event.ID, "dedup_key", event.DedupKey.String)
		s.updateNotificationEventStatus(ctx, event, storage.NotificationEventStatusSuppressed)
		return
	}

	decision, err := s.notificationRuleEngine.Evaluate(task, event)
	if err != nil {
		s.logger.Error("Failed to evaluate notification event", "event_id", event.ID, "error", err)
		s.updateNotificationEventStatus(ctx, event, storage.NotificationEventStatusRejected)
		return
	}

	switch decision.Status {
	case storage.NotificationEventStatusSuppressed, storage.NotificationEventStatusRejected:
		s.logger.Info("Notification event was not routed", "event_id", event.ID, "status", decision.Status, "reason", decision.Reason)
		s.updateNotificationEventStatus(ctx, event, decision.Status)
		return
	}

	routes, routeErr := s.notificationRouter.Resolve(task, decision.Channels)
	if routeErr != nil {
		s.logger.Warn("Notification router reported unresolved channels", "event_id", event.ID, "error", routeErr)
	}
	if len(routes) == 0 {
		s.updateNotificationEventStatus(ctx, event, storage.NotificationEventStatusRejected)
		return
	}

	for _, route := range routes {
		delivery := &storage.NotificationDelivery{
			ID:                  uuid.New(),
			NotificationEventID: event.ID,
			Channel:             route.Channel,
			Destination:         route.Destination,
			Status:              storage.NotificationDeliveryStatusPending,
			Attempt:             0,
			MaxAttempts:         route.MaxAttempts,
			ScheduledAt:         time.Now(),
		}
		if err := s.storage.Notification.CreateNotificationDelivery(ctx, delivery); err != nil {
			s.logger.Error("Failed to create notification delivery", "event_id", event.ID, "channel", route.Channel, "error", err)
			s.updateNotificationEventStatus(ctx, event, storage.NotificationEventStatusRejected)
			return
		}
		s.dispatchNotificationDelivery(ctx, event, delivery)
	}

	s.updateNotificationEventStatus(ctx, event, storage.NotificationEventStatusAccepted)
	s.logger.Info("Notification event routed", "event_id", event.ID, "delivery_count", len(routes))
}

func (e *NotificationRuleEngine) policyForTask(task *storage.Task) NotificationPolicy {
	if task == nil || !task.NotificationRuleSet.Valid || task.NotificationRuleSet.String == "" || task.NotificationRuleSet.String == "default" {
		return normalizeNotificationPolicy(e.policy)
	}

	switch task.NotificationRuleSet.String {
	case "all":
		return NotificationPolicy{
			DefaultChannels:   []string{"telegram"},
			DefaultSeverities: []string{"info", "warning", "error", "critical"},
			Rules:             e.policy.Rules,
		}
	case "errors_only":
		return NotificationPolicy{
			DefaultChannels:   []string{"telegram"},
			DefaultSeverities: []string{"error", "critical"},
			Rules:             e.policy.Rules,
		}
	case "disabled":
		return NotificationPolicy{}
	default:
		return normalizeNotificationPolicy(e.policy)
	}
}

func (r *NotificationRouter) resolveTaskDestinations(destinations []string) ([]NotificationRoute, error) {
	resolved := make([]NotificationRoute, 0, len(destinations))
	missing := make([]string, 0)

	for _, destination := range uniqueStrings(destinations) {
		channel, explicitTarget, hasExplicitTarget := strings.Cut(destination, ":")
		channel = strings.TrimSpace(channel)
		if channel == "" {
			continue
		}

		baseRoute, ok := r.routes[channel]
		if !ok {
			missing = append(missing, channel)
			continue
		}

		if hasExplicitTarget {
			baseRoute.Destination = strings.TrimSpace(explicitTarget)
		}
		resolved = append(resolved, baseRoute)
	}

	if len(missing) > 0 {
		return resolved, fmt.Errorf("missing routes for channels: %s", strings.Join(missing, ", "))
	}
	return resolved, nil
}

func (s *Server) shouldSuppressDuplicateEvent(ctx context.Context, event *storage.NotificationEvent) bool {
	if event == nil || !event.DedupKey.Valid || event.DedupKey.String == "" {
		return false
	}

	window := time.Duration(defaultDedupWindowSecond) * time.Second
	if event.DedupWindowSeconds.Valid && event.DedupWindowSeconds.Int32 > 0 {
		window = time.Duration(event.DedupWindowSeconds.Int32) * time.Second
	}

	since := time.Now().Add(-window)
	existing, err := s.storage.Notification.FindLatestNotificationEventByDedupKeySince(ctx, event.DedupKey.String, since, event.ID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false
		}
		s.logger.Warn("Failed to evaluate deduplication window", "event_id", event.ID, "dedup_key", event.DedupKey.String, "error", err)
		return false
	}

	return existing != nil
}

func (s *Server) updateNotificationEventStatus(ctx context.Context, event *storage.NotificationEvent, status storage.NotificationEventStatus) {
	if event.Status == status {
		return
	}
	if err := s.storage.Notification.UpdateNotificationEventStatus(ctx, event.ID, status); err != nil {
		s.logger.Error("Failed to update notification event status", "event_id", event.ID, "status", status, "error", err)
		return
	}
	event.Status = status
}

func payloadFromEvent(event *storage.NotificationEvent) (*alertPayload, error) {
	if len(event.PayloadJSON) == 0 {
		return nil, fmt.Errorf("notification event payload is empty")
	}

	var payload alertPayload
	if err := json.Unmarshal(event.PayloadJSON, &payload); err != nil {
		return nil, fmt.Errorf("invalid notification event payload: %w", err)
	}
	return &payload, nil
}

func (e *NotificationRuleEngine) channelsFromRules(policy NotificationPolicy, event *storage.NotificationEvent, payload *alertPayload) []string {
	channels := make([]string, 0)
	for _, rule := range policy.Rules {
		if !rule.Enabled {
			continue
		}
		if !matchesRule(rule, event, payload) {
			continue
		}
		channels = append(channels, rule.Channels...)
	}
	return channels
}

func matchesRule(rule NotificationRule, event *storage.NotificationEvent, payload *alertPayload) bool {
	if len(rule.EventTypes) > 0 && !slices.Contains(rule.EventTypes, event.EventType) {
		return false
	}
	if len(rule.Severities) > 0 && !slices.Contains(rule.Severities, event.Severity) {
		return false
	}
	if len(rule.Labels) == 0 {
		return true
	}
	for key, value := range rule.Labels {
		if payload.Labels[key] != value {
			return false
		}
	}
	return true
}

func normalizeNotificationPolicy(policy NotificationPolicy) NotificationPolicy {
	if len(policy.DefaultChannels) == 0 {
		policy.DefaultChannels = []string{"telegram"}
	}
	if len(policy.DefaultSeverities) == 0 {
		policy.DefaultSeverities = []string{"warning", "error", "critical"}
	}
	return policy
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}
