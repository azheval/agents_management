# Agent Management System

[![Build](https://github.com/azheval/agents_management/actions/workflows/build.yml/badge.svg)](https://github.com/azheval/agents_management/actions/workflows/build.yml)

Distributed task management system with centralized server and distributed agents.

[User guide English](/docs/guide.en.md)
[Кіраўніцтва карыстальніка Беларускi](/docs/guide.by.md)
[Руководство пользователя Русский](/docs/guide.ru.md)

![C4 Context](/docs/C4_Context.svg)

## Features

- Centralized task management
- Distributed agent architecture
- Support for various task types (commands, Python scripts, file transfers)
- Real-time monitoring and logging
- Scheduling capabilities
- Metrics collection

![C4 Container](/docs/C4_Container.svg)

![C4 Component](/docs/C4_Component.svg)

## Prerequisites

- Go 1.25+
- PostgreSQL
- Docker (for database management)

## Building

To build the server and agent:

```bash
# Ensure dependencies are resolved
make tidy

# Build both server and agent
make build
```

Alternatively, you can build components separately:

```bash
# Build server
make build-server

# Build agent for Windows
make build-agent-windows

# Build agent for Linux
make build-agent-linux
```

## Database Setup

The system uses PostgreSQL for data persistence. To set up the database:

```bash
# Start database container
make db-up

# Apply migrations for new installation
make db-migrate-fresh

# Or apply migrations incrementally (for updates)
make db-migrate
```

## Running

Start the server:

```bash
make run-server
```

Start an agent:

```bash
make run-agent
```

## Dependencies

This project uses Go modules for dependency management. All dependencies are listed in `go.mod` and will be downloaded automatically during build. No vendor directory is required for building.

## Project Structure

- `server/` - Server application code
- `agent/` - Agent application code
- `proto/` - Protocol buffer definitions
- `db/` - Database schema and migrations
- `docs/` - Documentation
