-- Create ENUM types for status fields
CREATE TYPE task_result_status AS ENUM ('SUCCESS', 'FAILED', 'TIMED_OUT');
CREATE TYPE log_level AS ENUM ('INFO', 'WARN', 'ERROR', 'DEBUG');

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

-- Create indexes for foreign keys and frequently queried columns
CREATE INDEX IF NOT EXISTS idx_task_results_task_id ON task_results(task_id);
CREATE INDEX IF NOT EXISTS idx_logs_task_id ON logs(task_id);
CREATE INDEX IF NOT EXISTS idx_logs_agent_id ON logs(agent_id);
CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON logs(timestamp);
