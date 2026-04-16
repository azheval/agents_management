.PHONY: all proto tidy build build-server build-server-linux build-agent-windows build-agent-linux run-server run-agent

all: build

vendor:
	go mod vendor

proto:
	protoc --proto_path=proto --go_out=./pkg/api --go_opt=paths=source_relative --go-grpc_out=./pkg/api --go-grpc_opt=paths=source_relative agent.proto task.proto

tidy:
	go mod tidy

CONFIG_SRC ?= server/config.json
CONFIG_DST ?= build/config.json

build: tidy build-server build-server-linux build-agent-windows

build-server:
	mkdir -p build
	cp $(CONFIG_SRC) $(CONFIG_DST)
	go build -o build/server.exe ./server/cmd/server

build-server-linux:
	mkdir -p build
	cp $(CONFIG_SRC) $(CONFIG_DST)
	GOOS=linux GOARCH=amd64 go build -o build/server ./server/cmd/server

build-agent-windows:
	mkdir -p build
	GOOS=windows GOARCH=amd64 go build -o build/agent.exe ./agent/cmd/agent

build-agent-linux:
	mkdir -p build
	GOOS=linux GOARCH=amd64 go build -o build/agent ./agent/cmd/agent

run-server:
	./build/server.exe

run-agent:
	./build/agent.exe

# Docker database management
db-up:
	docker-compose up -d

db-down:
	docker-compose down

db-logs:
	docker-compose logs -f

db-restart:
	docker-compose restart

db-migrate:
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/001_initial_schema.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/002_add_logs_and_results_tables.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/003_add_package_files_to_tasks.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/004_add_entrypoint_script_to_tasks.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/005_remove_script_content_from_tasks.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/006_change_package_files_to_text_array.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/007_add_agent_metrics_table.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/008_add_scheduling_to_tasks.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/009_add_notification_storage.sql
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/010_add_task_notification_settings.sql

# Quick migration for fresh installs
db-migrate-fresh:
	docker exec -i agent_db psql -U admin -d agent_management < db/migrations/001_full_schema.sql

