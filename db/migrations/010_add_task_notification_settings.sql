ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS result_contract VARCHAR(64),
    ADD COLUMN IF NOT EXISTS notification_rule_set VARCHAR(64),
    ADD COLUMN IF NOT EXISTS default_destinations TEXT[];
