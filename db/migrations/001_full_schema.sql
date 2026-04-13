-- Full schema for fresh installation (consolidated from all individual migrations)
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- Create ENUM types for status fields
CREATE TYPE agent_status AS ENUM ('online', 'offline', 'disconnected');
CREATE TYPE task_status AS ENUM ('pending', 'assigned', 'running', 'completed', 'failed', 'timed_out');
CREATE TYPE task_type AS ENUM ('EXEC_COMMAND', 'EXEC_PYTHON_SCRIPT', 'FETCH_FILE', 'PUSH_FILE', 'AGENT_UPDATE');
CREATE TYPE task_result_status AS ENUM ('SUCCESS', 'FAILED', 'TIMED_OUT');
CREATE TYPE log_level AS ENUM ('INFO', 'WARN', 'ERROR', 'DEBUG');

-- Table: agents
CREATE TABLE IF NOT EXISTS agents (
    id UUID PRIMARY KEY,
    hostname VARCHAR(255) NOT NULL,
    os VARCHAR(100),
    arch VARCHAR(50),
    agent_version VARCHAR(50),
    ip_addresses TEXT[],
    last_heartbeat TIMESTAMPTZ,
    status agent_status DEFAULT 'disconnected',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Table: tasks
CREATE TABLE IF NOT EXISTS tasks (
    id UUID PRIMARY KEY,
    agent_id UUID REFERENCES agents(id) ON DELETE SET NULL,
    description TEXT,
    task_type task_type NOT NULL,
    command TEXT,
    args TEXT[],
    source_path VARCHAR(1024),
    destination_path VARCHAR(1024),
    timeout_seconds INT,
    status task_status DEFAULT 'pending',
    scheduled_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_by VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    package_files TEXT[],
    entrypoint_script VARCHAR(255),
    result_contract VARCHAR(64),
    notification_rule_set VARCHAR(64),
    default_destinations TEXT[],
    schedule_type VARCHAR(20),
    cron_expression VARCHAR(255),
    prerequisite_task_id UUID REFERENCES tasks(id) ON DELETE SET NULL
);

-- Table: task_results
CREATE TABLE IF NOT EXISTS task_results (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status task_result_status NOT NULL,
    exit_code INT,
    output TEXT,
    output_file_path VARCHAR(1024),
    duration_ms BIGINT,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Table: logs
CREATE TABLE IF NOT EXISTS logs (
    id UUID PRIMARY KEY,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    task_id UUID REFERENCES tasks(id) ON DELETE CASCADE,
    timestamp TIMESTAMPTZ NOT NULL,
    level log_level NOT NULL,
    message TEXT
);

-- Table: agent_metrics
CREATE TABLE IF NOT EXISTS agent_metrics (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL,
    timestamp TIMESTAMPTZ NOT NULL,
    cpu_usage REAL,
    ram_usage REAL,
    disk_usage JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_agent
        FOREIGN KEY(agent_id) 
        REFERENCES agents(id)
        ON DELETE CASCADE
);

-- Table: notification_events
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

-- Table: notification_deliveries
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

-- Table: users
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(128) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Table: roles
CREATE TABLE IF NOT EXISTS roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Table: user_roles
CREATE TABLE IF NOT EXISTS user_roles (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, role_id)
);

-- Create indexes for foreign keys and frequently queried columns
CREATE INDEX IF NOT EXISTS idx_tasks_agent_id ON tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
CREATE INDEX IF NOT EXISTS idx_agents_last_heartbeat ON agents(last_heartbeat);
CREATE INDEX IF NOT EXISTS idx_task_results_task_id ON task_results(task_id);
CREATE INDEX IF NOT EXISTS idx_logs_task_id ON logs(task_id);
CREATE INDEX IF NOT EXISTS idx_logs_agent_id ON logs(agent_id);
CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_agent_metrics_agent_id_timestamp ON agent_metrics (agent_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_notification_events_task_id ON notification_events(task_id);
CREATE INDEX IF NOT EXISTS idx_notification_events_agent_id ON notification_events(agent_id);
CREATE INDEX IF NOT EXISTS idx_notification_events_event_type ON notification_events(event_type);
CREATE INDEX IF NOT EXISTS idx_notification_events_dedup_key ON notification_events(dedup_key);
CREATE INDEX IF NOT EXISTS idx_notification_events_created_at ON notification_events(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_event_id ON notification_deliveries(notification_event_id);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_status ON notification_deliveries(status);
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_next_retry_at ON notification_deliveries(next_retry_at);
CREATE INDEX IF NOT EXISTS idx_user_roles_user_id ON user_roles(user_id);
CREATE INDEX IF NOT EXISTS idx_user_roles_role_id ON user_roles(role_id);

-- Add triggers to automatically update the updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
   NEW.updated_at = NOW();
   RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_agents_updated_at
BEFORE UPDATE ON agents
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_tasks_updated_at
BEFORE UPDATE ON tasks
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_users_updated_at
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();

INSERT INTO roles (name, description)
VALUES
    ('admin', 'Full access to all agents and actions'),
    ('full_access', 'Full access to all agents and actions without the admin label'),
    ('action.exec_command', 'Allows creating and managing EXEC_COMMAND tasks'),
    ('action.exec_python_script', 'Allows creating and managing EXEC_PYTHON_SCRIPT tasks'),
    ('action.fetch_file', 'Allows creating and managing FETCH_FILE tasks'),
    ('action.push_file', 'Allows creating and managing PUSH_FILE tasks'),
    ('action.agent_update', 'Allows creating and managing AGENT_UPDATE tasks')
ON CONFLICT (name) DO NOTHING;

INSERT INTO users (username, password_hash, is_active)
SELECT 'admin', crypt('admin', gen_salt('bf')), TRUE
WHERE NOT EXISTS (
    SELECT 1 FROM users WHERE username = 'admin'
);

INSERT INTO user_roles (user_id, role_id)
SELECT u.id, r.id
FROM users u
JOIN roles r ON r.name = 'admin'
WHERE u.username = 'admin'
ON CONFLICT (user_id, role_id) DO NOTHING;

COMMENT ON TABLE agent_metrics IS 'Stores historical system metrics from agents.';
COMMENT ON COLUMN agent_metrics.agent_id IS 'Foreign key to the agents table.';
COMMENT ON COLUMN agent_metrics.timestamp IS 'The timestamp when the metric was collected on the agent.';
COMMENT ON COLUMN agent_metrics.cpu_usage IS 'CPU usage percentage.';
COMMENT ON COLUMN agent_metrics.ram_usage IS 'RAM usage percentage.';
COMMENT ON COLUMN agent_metrics.disk_usage IS 'JSONB object storing disk usage per mount point.';
