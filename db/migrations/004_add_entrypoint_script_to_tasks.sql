-- Add entrypoint_script column to tasks table
ALTER TABLE tasks
  ADD COLUMN entrypoint_script VARCHAR(255);
