# Agent Management

## Database

To start the database container, use the command

```make
make db-up
```

or

```cmd
docker-compose up -d
```

On the first run, you need to create database tables by running migration

```make
make db-migrate-fresh
```

or

```cmd
docker exec -i agent_db psql -U admin -d agent_management < db/migrations/001_full_schema.sql
```

## Starting Server and Agent

To start the server, use the command

```cmd
<path>/server.exe -config <path>/config.json
```

An example of the `config.json` file is in `server/config.json`.

To start the agent, use the command

```cmd
<path>/agent.exe
```

When starting the agent, a service file `agent.id` with the agent's unique identifier is created.

## Management Interface

### Managing Agents

By default, the management interface is available on port 8080. The port is set in the `config.json` file with the setting

```json
"webserver": {"listen_address": ":8080"}
```

![img_001.png](/docs/img/img_001.png)

### Access Control

The web interface uses username and password authentication. User accounts, roles, and role assignments are stored in the database tables `users`, `roles`, and `user_roles`.

Permissions are configured in the `Access` section.

![img_013.png](/docs/img/img_013.png)

The system uses four groups of permissions:

- `admin` and `full_access` grant full access to all sections and all agents.
- Global `action.*` roles define which task types a user is allowed to create.
- Permissions of the form `agent:<uuid>:...` define which actions are allowed for a specific agent.
- EXEC_COMMAND policy permissions `exec_policy:<uuid>:use|view|manage` define access to saved command policies.

To create a task, a user must have both:

- the global action role matching the task type, for example `action.exec_command`;
- the `agent:<uuid>:task_create` permission for the selected agent.
- `exec_policy:<policy_uuid>:use`

To view resolved command and arguments of a policy-backed task, the user must additionally have:

- `exec_policy:<policy_uuid>:view`

The permission matrix in the `Access` section lets you assign rights at the intersection of user, agent, and operation. It supports separate permissions for:

- viewing the agent;
- viewing metrics;
- viewing tasks;
- creating tasks;
- rescheduling tasks;
- viewing notifications;
- toggling agent status;
- deleting the agent.

## EXEC_COMMAND Policies

Saved EXEC_COMMAND policies are designed for secure and reusable command execution.

A policy stores:

- policy name and description
- command template
- args template
- parameter schema

A binding stores values for a specific agent:

- target agent
- optional command template override
- optional args template override
- parameter values JSON

Templates use double braces:

- `{{target}}`
- `{{count}}`

Example policy for `ping`:

- Command Template: `ping`
- Args Template: `-n,{{count}},{{target}}`

Example parameter schema:

```json
[
  {
    "name": "target",
    "label": "Target",
    "type": "text",
    "required": true,
    "editable": false
  },
  {
    "name": "count",
    "label": "Count",
    "type": "number",
    "required": true,
    "editable": true,
    "default_value": "4"
  }
]
```

Example binding for agent A:

```json
{
  "target": "127.0.0.1"
}
```

After resolution the task will execute:

```text
ping -n 4 127.0.0.1
```

![img_014.png](/docs/img/img_014.png)

![img_015.png](/docs/img/img_015.png)

Recommended setup flow:

1. Create a user in the `Access` section.
2. If the user needs unrestricted access, assign `admin` or `full_access`.
3. If restricted access is needed, assign only the required global `action.*` roles.
4. In the matrix, mark permissions only for the agents and operations the user actually needs.
5. Verify the result by signing in with the new account.

The `agent:<uuid>` role is kept for backward compatibility with older configurations and means broad access to the agent. For new setups, the fine-grained permission matrix is recommended.

### Creating Tasks

A new task is created from the `Tasks` form using the `Create new task` command.
In the task creation form, agents that will execute the task are selected. For each selected agent, its own unique task will be created.
The task type is specified. For each task type, unique fields need to be filled in.

#### EXEC_COMMAND

![img_002.png](/docs/img/img_002.png)

Fill in the task description, command (for example, `C:/ws/tech_log/go/techlog-stat/.dist/techlog-stat.exe`), command arguments are specified separated by commas (for example, `sdbl-context,--input,C:/ws/tech_log/logs,--glob,*/*.*,--output,C:/ws/tech_log/out/sdbl_agent,--top,10`).

When using templates, simply select a template.

![img_016.png](/docs/img/img_016.png)

#### EXEC_PYTHON_SCRIPT

![img_003.png](/docs/img/img_003.png)

Fill in the task description, select the necessary files (not only python files), specify the orchestrator file (Entrypoint) that will be called by the agent for execution.
All specified files will be placed in an archive when creating the task, transferred to the agent, extracted and executed.
**Important**. Python must be pre-installed on the remote workplace.

#### FETCH_FILE

![img_004.png](/docs/img/img_004.png)

Fill in the task description, path to the file for the agent, path where the server will save the file.

#### PUSH_FILE

![img_005.png](/docs/img/img_005.png)

Fill in the task description, path for saving the file by the agent, files transmitted by the server.

#### AGENT_UPDATE

![img_006.png](/docs/img/img_006.png)

Fill in the task description, specify the new agent version file. The checksum is calculated automatically.

Any task can be configured with an 'Alert Pipeline' block that sends the 'stdout' stream to Telegram.

![img_010.png](/docs/img/img_010.png)

![img_011.png](/docs/img/img_011.png)

Select the required task execution schedule, specify the timeout for command execution:

- Immediate. The task executes immediately.
- Scheduled. The task will be executed at the specified date and time.
- Recurring. The task will be executed periodically according to the specified schedule.
- Chained. The task will be executed after the previous (selected) task completes.

### Task List

![img_007.png](/docs/img/img_007.png)

### Task Execution Log

![img_008.png](/docs/img/img_008.png)

## Notifications

When filling in the `config.json` file section ```telegram {}``` a notification about task completion will be received

![img_009.png](/docs/img/img_009.png)

## Notifications Alert Pipeline

![img_012.png](/docs/img/img_012.png)
