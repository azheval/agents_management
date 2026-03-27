# Database Schema and Migrations

## Overview

This directory contains all database migration files for the agent management system. The system uses PostgreSQL as its database backend.

## Migration Strategy

### Current Approach

- Migrations are applied manually using the Makefile targets
- Each migration file represents a specific schema change
- Migrations are applied sequentially in numerical order

### Available Commands

- `make db-migrate`: Apply all migrations in sequence (for incremental updates)
- `make db-migrate-fresh`: Apply the consolidated schema for fresh installations

### Migration Files

1. `001_initial_schema.sql` - Base schema with agents and tasks tables
2. `002_add_logs_and_results_tables.sql` - Adds task_results and logs tables
3. `003_add_package_files_to_tasks.sql` - Adds package_files column
4. `004_add_entrypoint_script_to_tasks.sql` - Adds entrypoint_script column
5. `005_remove_script_content_from_tasks.sql` - Removes obsolete script_content column
6. `006_change_package_files_to_text_array.sql` - Changes package_files from JSONB to TEXT[]
7. `007_add_agent_metrics_table.sql` - Adds agent_metrics table
8. `008_add_scheduling_to_tasks.sql` - Adds scheduling columns to tasks
9. `001_full_schema.sql` - Consolidated schema for fresh installations

## Server Startup Behavior

- The server does NOT automatically apply migrations
- Database connection is established first, then repositories are created
- If required tables don't exist, the server will fail at startup
- Manual migration application is required before starting the server with a new database

## Recommendations

For new installations, use the `db-migrate-fresh` target which applies the consolidated schema in one step. For existing databases that need to be updated incrementally, use the `db-migrate` target.

All migration commands use relative paths from the project root directory, making them portable across different environments.
