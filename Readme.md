# AgentWisp

AgentWisp is a lightweight macOS CLI for creating and managing isolated
development workspaces with Apple's `container` command.

Each project directory gets its own container. The host directory is mounted at
`/workspace`, allowing development tools to run in isolation while source files
remain on the host.

## Concept

AgentWisp provides two workflows:

- Run `agentwisp add` inside a project to create or start its workspace.
- Run `agentwisp` from anywhere to manage all AgentWisp workspaces in an
  interactive terminal UI.

Containers are identified by the `agentwisp-work-` prefix and store the original
workspace path in a container label. Legacy `codex-work-*` containers created
before the AgentWisp rename remain discoverable and manageable.

## Requirements

- macOS
- Apple [`container`](https://github.com/apple/container)
- Go toolchain for building from source
- A terminal with ANSI color and alternate-screen support

## Build

```sh
gofmt -w main.go main_test.go
go test ./...
go build -o agentwisp .
```

## Install

Install the binary so it is available from any directory:

```sh
sudo install -m 755 ./agentwisp /usr/local/bin/agentwisp
rehash
```

Verify the installed command:

```sh
command -v agentwisp
agentwisp help
```

If upgrading from the old `codexbox` name, the previous binary can be removed
after AgentWisp is installed:

```sh
sudo rm -f /usr/local/bin/codexbox
```

Existing `codex-work-*` workspace containers do not need to be recreated.

## Usage

Open the workspace manager:

```sh
agentwisp
```

Add the current directory as a workspace, start it, and enter its shell:

```sh
agentwisp add
```

Start or enter the current directory workspace:

```sh
agentwisp start
```

Other commands:

```text
agentwisp shell    Enter the current directory workspace
agentwisp stop     Stop the current directory workspace
agentwisp rm       Remove the current directory workspace
agentwisp help     Show CLI and TUI help
```

Compatibility aliases:

```text
run               Alias for start
list, ls, .       Open the workspace manager
```

## Workspace manager

The TUI displays:

- workspace/container name;
- current status;
- Enter action (`ATTACH` or `START`);
- host volume path.

Controls:

```text
Up/Down or J/K    Move selection
Enter             Attach to running or start stopped workspace
S                 Stop selected workspace
R                 Refresh statuses
Q or Esc          Quit
```

The interface adapts to terminal width and keeps a compact fallback for narrow
windows.

## Container configuration

New workspaces currently use:

```text
Image:       docker.io/library/node:22-bookworm
Mount:       <host-project>:/workspace
Workdir:     /workspace
Shell:       bash
Name:        agentwisp-work-<sanitized-directory-name>
```

Labels:

```text
dev.agentwisp.managed=true
dev.agentwisp.workspace=<host-project>
```

Legacy labels under `dev.codexbox.*` are read for migration compatibility but
are not written to new containers.

## Example workflow

```sh
cd ~/projects/my-api
agentwisp add
```

Later, from any directory:

```sh
agentwisp
```

Select `agentwisp-work-my-api` and press Enter to attach. If it is stopped, AgentWisp
starts it first.

## Security

The project intentionally uses only the Go standard library. Dependency review
requirements and container security guidance are documented in
[`security.md`](security.md).

A mounted workspace is accessible from inside its container. Do not mount
sensitive parent directories or use untrusted projects without reviewing them.

## Development

Development rules and TUI invariants are documented in
[`Agent.md`](Agent.md).

Before submitting changes:

```sh
gofmt -w main.go main_test.go
go vet ./...
go test ./...
go build -o agentwisp .
```

## Current limitations

- Apple `container` and macOS are required.
- The image is currently hardcoded.
- Workspace identity is based on the sanitized directory name, so directories
  with the same basename can collide.
- The TUI supports starting, attaching, stopping, and refreshing, but removal is
  currently available only through `agentwisp rm` in the project directory.
- Container image tags are not yet pinned to a digest.
