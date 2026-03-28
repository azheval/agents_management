CREATE TABLE IF NOT EXISTS notification_events (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    prerequisite_task_id UUID REFERENCES tasks(id) ON DELETE SET NULL,
    event_type VARCHAR(255) NOT NULL,
    severity VARCHAR(32) NOT NULL,
    title VARCHAR(255) NOT NULL,
    summary TEXT NOT NULL,
    source_kind VARCHAR(64) NOT NULL,
    source_path VARCHAR(1024),
    source_ref VARCHAR(1024),
    payload_json JSONB NOT NULL,
    dedup_key VARCHAR(255),
    dedup_window_seconds INT,
    status VARCHAR(32) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id UUID PRIMARY KEY,
    notification_event_id UUID NOT NULL REFERENCES notification_events(id) ON DELETE CASCADE,
    channel VARCHAR(64) NOT NULL,
    destination VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL,
    attempt INT NOT NULL DEFAULT 1,
    max_attempts INT NOT NULL DEFAULT 5,
    provider_message_id VARCHAR(255),
    provider_response_json JSONB,
    error_message TEXT,
    last_error_code VARCHAR(128),
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notification_events_task_id ON notification_events(task_id);
CREATE INDEX IF NOT EXISTS idx_notification_events_agent_id ON notification_events(agent_id);
CREATE INDEX IF NOT EXISTS idx_notification_events_event_type ON notification_events(event_type);
CREATE INDEX IF NOT EXISTS idx_notification_events_dedup_key ON notification_events(dedup_key);
CREATE INDEX IF NOT EXISTS idx_notification_events_created_at ON notification_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_event_id ON notification_deliveries(notification_event_id);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_status ON notification_deliveries(status);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_next_retry_at ON notification_deliveries(next_retry_at);
