CREATE TABLE IF NOT EXISTS exec_command_policies (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    command_template TEXT NOT NULL,
    args_template TEXT[] NOT NULL DEFAULT '{}',
    parameter_schema JSONB NOT NULL DEFAULT '[]'::jsonb,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS exec_command_policy_bindings (
    id UUID PRIMARY KEY,
    policy_id UUID NOT NULL REFERENCES exec_command_policies(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    command_template_override TEXT,
    args_template_override TEXT[] NOT NULL DEFAULT '{}',
    parameter_values JSONB NOT NULL DEFAULT '{}'::jsonb,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT exec_command_policy_bindings_policy_agent_unique UNIQUE (policy_id, agent_id)
);

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS exec_policy_id UUID REFERENCES exec_command_policies(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS exec_policy_binding_id UUID REFERENCES exec_command_policy_bindings(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_exec_command_policies_is_active ON exec_command_policies(is_active);
CREATE INDEX IF NOT EXISTS idx_exec_command_policy_bindings_policy_id ON exec_command_policy_bindings(policy_id);
CREATE INDEX IF NOT EXISTS idx_exec_command_policy_bindings_agent_id ON exec_command_policy_bindings(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_exec_policy_id ON tasks(exec_policy_id);

CREATE TRIGGER update_exec_command_policies_updated_at
BEFORE UPDATE ON exec_command_policies
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_exec_command_policy_bindings_updated_at
BEFORE UPDATE ON exec_command_policy_bindings
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();
