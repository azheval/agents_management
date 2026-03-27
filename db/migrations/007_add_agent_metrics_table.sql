-- +migrate Up
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

CREATE INDEX IF NOT EXISTS idx_agent_metrics_agent_id_timestamp ON agent_metrics (agent_id, timestamp DESC);
COMMENT ON TABLE agent_metrics IS 'Stores historical system metrics from agents.';
COMMENT ON COLUMN agent_metrics.agent_id IS 'Foreign key to the agents table.';
COMMENT ON COLUMN agent_metrics.timestamp IS 'The timestamp when the metric was collected on the agent.';
COMMENT ON COLUMN agent_metrics.cpu_usage IS 'CPU usage percentage.';
COMMENT ON COLUMN agent_metrics.ram_usage IS 'RAM usage percentage.';
COMMENT ON COLUMN agent_metrics.disk_usage IS 'JSONB object storing disk usage per mount point.';


-- +migrate Down
DROP INDEX IF EXISTS idx_agent_metrics_agent_id_timestamp;
DROP TABLE IF EXISTS agent_metrics;
