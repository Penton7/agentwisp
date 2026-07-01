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
gofmt -w cmd/agentwisp internal/agentwisp
go test ./...
go build -o agentwisp ./cmd/agentwisp
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
agentwisp monitor  Show traffic monitor status for the current workspace
agentwisp monitor on
agentwisp monitor off
agentwisp monitor logs
agentwisp monitor clear
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
- host volume path;
- selected workspace proxy status when the traffic monitor is enabled.

Controls:

```text
Up/Down or J/K    Move selection
Enter             Attach to running or start stopped workspace
S                 Stop selected workspace and its traffic monitor proxy
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

## Experimental traffic monitor

AgentWisp can run an opt-in metadata-only HTTP(S) proxy for a workspace:

```sh
agentwisp monitor on
agentwisp shell
```

When enabled, AgentWisp starts a local proxy on the host and enters the
workspace with `HTTP_PROXY`, `HTTPS_PROXY`, and matching lowercase variables set.
The proxy URL includes a per-workspace random token and the proxy requires that
token through `Proxy-Authorization`.
Logs are stored under:

```text
~/.agentwisp/workspaces/<workspace>/traffic.ndjson
```

View or clear logs:

```sh
agentwisp monitor logs
agentwisp monitor clear
agentwisp monitor off
```

Private LAN destinations are blocked by default to prevent the workspace from
using the monitor proxy to reach local network devices or services through the
host laptop. If you explicitly need LAN access through the proxy:

```sh
agentwisp monitor on --allow-private
```

The monitor logs metadata only:

- timestamp;
- workspace;
- scheme;
- host;
- port;
- method;
- byte counts;
- allow/deny result.

It does not log headers, cookies, Authorization values, request bodies, response
bodies, or full URLs by default.

For Apple `container`, AgentWisp tries to detect the host gateway from inside
the container with `ip route show default`. If that does not work on your setup,
override it:

```sh
AGENTWISP_PROXY_HOST=<host-gateway-name-or-ip> agentwisp shell
```

By default, the host proxy listens on `0.0.0.0` so Apple containers can reach it.
If you know the specific host-side interface address that containers can reach,
bind only to that address:

```sh
AGENTWISP_PROXY_LISTEN_HOST=<host-interface-ip> agentwisp monitor on
```

Do not use Docker-specific hostnames here; AgentWisp is using Apple's
`container` CLI.

This monitor is experimental. It only sees traffic from tools that honor
`HTTP_PROXY` / `HTTPS_PROXY`.

Security defaults:

- proxy authentication is required;
- private LAN destinations are blocked unless `--allow-private` is used;
- loopback, link-local, multicast, and unspecified destinations are always blocked;
- HTTP `CONNECT` is restricted to port `443`;
- proxy process health/shutdown uses authenticated local control requests.

## Development

Development rules and TUI invariants are documented in
[`Agent.md`](Agent.md).

Before submitting changes:

```sh
gofmt -w cmd/agentwisp internal/agentwisp
go vet ./...
go test ./...
go build -o agentwisp ./cmd/agentwisp
```

## Current limitations

- Apple `container` and macOS are required.
- The image is currently hardcoded.
- Workspace identity is based on the sanitized directory name, so directories
  with the same basename can collide.
- The TUI supports starting, attaching, stopping, and refreshing, but removal is
  currently available only through `agentwisp rm` in the project directory.
- Container image tags are not yet pinned to a digest.
