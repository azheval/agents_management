-- +migrate Up
ALTER TABLE tasks
    ADD COLUMN schedule_type VARCHAR(20),
    ADD COLUMN cron_expression VARCHAR(255),
    ADD COLUMN prerequisite_task_id UUID REFERENCES tasks(id);

-- +migrate Down
--ALTER TABLE tasks
--    DROP COLUMN schedule_type,
--    DROP COLUMN cron_expression,
--    DROP COLUMN prerequisite_task_id;
