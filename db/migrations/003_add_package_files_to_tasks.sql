-- Add package_files column to tasks table to store a list of files for a task
ALTER TABLE tasks ADD COLUMN package_files JSONB;
