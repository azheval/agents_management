CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username VARCHAR(128) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS roles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_roles (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, role_id)
);

CREATE INDEX IF NOT EXISTS idx_user_roles_user_id ON user_roles(user_id);
CREATE INDEX IF NOT EXISTS idx_user_roles_role_id ON user_roles(role_id);

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

CREATE TRIGGER update_users_updated_at
BEFORE UPDATE ON users
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();
