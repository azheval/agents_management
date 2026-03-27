-- Create ENUM types for status fields
CREATE TYPE agent_status AS ENUM ('online', 'offline', 'disconnected');
CREATE TYPE task_status AS ENUM ('pending', 'assigned', 'running', 'completed', 'failed', 'timed_out');
CREATE TYPE task_type AS ENUM ('EXEC_COMMAND', 'EXEC_PYTHON_SCRIPT', 'FETCH_FILE', 'PUSH_FILE', 'AGENT_UPDATE');

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
    script_content BYTEA,
    source_path VARCHAR(1024),
    destination_path VARCHAR(1024),
    timeout_seconds INT,
    status task_status DEFAULT 'pending',
    scheduled_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_by VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Create indexes for foreign keys and frequently queried columns
CREATE INDEX IF NOT EXISTS idx_tasks_agent_id ON tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
CREATE INDEX IF NOT EXISTS idx_agents_last_heartbeat ON agents(last_heartbeat);

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
