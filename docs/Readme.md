# AgentWisp documentation

## Using AgentWisp

- [Installation and CLI usage](../Readme.md)
- [Building the default Claude Code and Codex image](../images/agents/Readme.md)
- [Security policy and container boundaries](../security.md)

## Architecture

- [Workspaces, agent sessions, and attachments](core-models.md)
- [Workspace service and runtime adapter](runtime-service.md)

The architecture documents distinguish implemented behavior from planned
capabilities. Managed background sessions, reconnect, and a live Web UI are not
available yet.

## Documentation conventions

Public documentation is written in English. Local planning, research, and audit
notes use an `_internal` filename suffix, such as `topic_internal.md`, and are
excluded by `.gitignore`. Public documents must not link to these local files.
