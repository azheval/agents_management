-- Remove obsolete script_content column from tasks table
ALTER TABLE tasks DROP COLUMN IF EXISTS script_content;
