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
- Go 1.27.1 or newer for building from source
- A terminal with ANSI color and alternate-screen support

## Build

Check the selected toolchain first:

```sh
type -a go
go version
```

Use Go 1.27.1 or newer. If `go test` reports
`invalid go version '1.27.1': must match format 1.23`, the selected Go command
has an older module parser. The `1.23` in that message is a format example,
not the required version. Patch versions in the `go` directive are valid in
[modern Go](https://go.dev/doc/toolchain).

On macOS, install a current Go `.pkg` from the
[official downloads](https://go.dev/dl/). The official installer uses
`/usr/local/go`. To select that installation in the current shell:

```sh
export PATH="/usr/local/go/bin:$PATH"
hash -r
type -a go
go version
```

If Go is managed by Homebrew, asdf, or mise, update/select it through that
manager instead. An old executable earlier in `PATH` can shadow a new install.
Keep `go.mod` unchanged: dropping the patch version does not update the compiler
or its standard library. After verifying the selected version, build:

```sh
gofmt -w cmd/agentwisp internal/agentwisp
go test ./...
go build -o agentwisp ./cmd/agentwisp
go build -o agw ./cmd/agw
```

## Install

Install the binary so it is available from any directory:

```sh
sudo install -m 755 ./agentwisp /usr/local/bin/agentwisp
sudo install -m 755 ./agw /usr/local/bin/agw
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
agentwisp rm       Remove current workspace after typing its container name
agentwisp rm --force --yes  Remove even if running, without a prompt
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
D                 Delete selected workspace (Y confirms; any other key cancels)
R                 Refresh statuses
Q or Esc          Quit
```

The interface adapts to terminal width and keeps a compact fallback for narrow
windows.

## JSON workspace inventory

Use read-only JSON output for scripts:

```sh
agw list --json
agw inspect <workspace-id> --json
```

Copy the full `id` from the list response. Both responses include
`schema_version: 1`; errors go to stderr with a nonzero exit code. Without
`--json`, `agw list` still opens the workspace manager. See the
[JSON command reference](docs/json-inventory.md) for fields and examples.

## Container configuration

New workspaces currently use:

```text
Image:       agentwisp-agents:local (locally built from images/agents)
Mount:       <host-project>:/workspace
Workdir:     /workspace
Shell:       bash
Name:        agentwisp-work-<sanitized-directory-name>-<path-hash>
```

Build the default image once from the AgentWisp repository root, supplying exact
versions for both agents:

```sh
: "${CODEX_VERSION:?Set CODEX_VERSION to a reviewed X.Y.Z version}"
: "${CLAUDE_VERSION:?Set CLAUDE_VERSION to a reviewed X.Y.Z version}"
container build \
  --build-arg CODEX_VERSION="$CODEX_VERSION" \
  --build-arg CLAUDE_VERSION="$CLAUDE_VERSION" \
  -t agentwisp-agents:local images/agents
```

Then run `agw add` in a new project directory. No `AGENTWISP_IMAGE` setting is
needed. Existing workspaces keep their original image, even after stop/start
or rebuilding the tag. To override the image for a new workspace, use
`AGENTWISP_IMAGE=<image:tag> agw add`. The image is not built automatically.

Labels:

```text
dev.agentwisp.managed=true
dev.agentwisp.workspace=<host-project>
```

Legacy labels under `dev.codexbox.*` are read for migration compatibility but
are not written to new containers.

New containers are created detached with a small keep-alive main process; every
shell and agent is a separate `exec` session. Exiting one shell no longer stops
the container under agents running in other panes. Workspaces created by
earlier versions still use their first `bash` as the main process; recreate them
(after exporting any container-only data) to get this behavior.

## Example workflow

```sh
cd ~/projects/my-api
agentwisp add
```

Later, from any directory:

```sh
agentwisp
```

Select the `agentwisp-work-my-api-...` workspace and press Enter to attach. If it is stopped, AgentWisp
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

By default, the host proxy listens only on the host's address on the workspace
container network: AgentWisp reads the container's address from `container
inspect` and picks the host interface on the same subnet. It is not exposed on
Wi-Fi or other LAN interfaces. The workspace receives that address directly, so
gateway detection is only a fallback. If the workspace is not running,
`monitor on` saves the setting and the proxy starts with the next
`agw shell`/`agw agent`. Proxy startup errors are written to
`~/.agentwisp/workspaces/<workspace>/proxy.log`.

If detection does not work on your setup, choose the bind address explicitly
(`0.0.0.0` restores the old, broader behavior):

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
go build -o agw ./cmd/agw
```

## Current limitations

- Apple `container` and macOS are required.
- Real runtime and filesystem behavior must be verified on macOS with Apple `container`.
- The default image tag is mutable; use `AGENTWISP_IMAGE` with a reviewed digest for releases.
- The traffic proxy observes cooperating HTTP clients; it is not an egress firewall.
- The source bind mount is writable, including `.git` and any project secrets.
- TUI paths containing non-ASCII or control characters use escaped notation to preserve terminal safety and alignment.
- Large workspace lists do not yet have vertical scrolling or search.
- Monitor lifecycle is serialized per workspace with a file lock; container start/stop/remove are not yet serialized across processes.

## Removing workspaces

Press `D` in the manager to see the selected container and the deletion scope.
`Y` confirms removal, including a running container; any other key cancels.
The manager stays open after deleting the last workspace (`R` refreshes).

From the project directory, `agentwisp rm` asks you to type the container name.
A running workspace requires `--force`; `--yes` skips the prompt for scripts.
`remove` is an alias for `rm`. The proxy must shut down successfully before
container deletion proceeds. An already-exited proxy is tolerated; authentication
and timeout errors retain state for retry.

Deletion removes the container filesystem, installed tools, and credentials
stored inside it. It preserves the host project directory, container images,
and host traffic logs. Clear retained logs explicitly before removing the
workspace with `agentwisp monitor clear`, or manage the retained files in the
state directory. Deletion does not imply secure erasure from backups.

New container names include a hash of the canonical project path. Existing
AgentWisp and Codexbox containers are reused only when their stored workspace
path matches. An ambiguous path mapping is reported instead of selecting an
arbitrary container.

## Preinstalled Codex

A build recipe is provided at [`images/codex/Dockerfile`](images/codex/Dockerfile).
It installs Codex once into an image, uses the unprivileged `node` user, and
contains no credentials or project files. Set `CODEX_VERSION` to an exact version
you have reviewed; the recipe rejects an omitted version or a mutable tag.

For example, `0.153.4` is the version installed in the environment where this
change was developed; it is not a promise that this is the latest or audited
release. From the AgentWisp repository root:

```sh
container build --build-arg CODEX_VERSION=0.153.4 -t agentwisp-codex:local images/codex
```

Then, from a project directory that does not yet have a workspace:

```sh
AGENTWISP_IMAGE=agentwisp-codex:local agentwisp add
```

Inside the workspace:

```sh
codex --version
codex login --device-auth
codex
```

Device authentication may need to be enabled in ChatGPT account/workspace
settings. See [official Codex authentication documentation](https://learn.chatgpt.com/docs/auth).
Credentials and sessions remain in the container user's home across stop/start;
removing the container loses them. AgentWisp does not mount the host's `~/.codex`,
SSH agent, or cloud credentials. Do not put keys in build arguments or image layers.

`AGENTWISP_IMAGE` affects newly created workspaces only. Existing containers keep
their image and data. To migrate, first export any needed container-only data,
then remove/recreate the workspace. Host source files remain in place.
For reproducible images, also pass a reviewed `BASE_IMAGE` digest at build time
and record the resulting image digest. `USER node` mount permissions need a
smoke test on your Apple container version; do not solve a mismatch by applying
recursive permission changes to the host project.

## Monitor hardening and limits

Proxy credentials are supplied via a temporary `0600` environment file rather
than shell/process arguments. The file is removed after the interactive process
exits; a host process crash can leave it in the OS temporary directory. Credentials
are still visible to processes inside the workspace. New workspaces receive the
environment file only on `exec`, not at container creation. This is not isolation
from other processes of the same host user.

Config writes are atomic. Monitor startup waits for an authenticated health
response. Custom IP bindings are persisted for later control requests. The proxy
rejects control redirects, strips all `Connection` header values, and preserves
buffered CONNECT data. Limits: 64 concurrent forwarded requests/tunnels,
30-minute maximum tunnel lifetime, 30-second upstream HTTP header timeout,
8 MiB per traffic log with one rotated `.1` file (a record can exceed the limit).
`monitor logs` shows the active file; `monitor clear` clears both files.

See the [documentation index](docs/Readme.md) for usage and architecture, and
the [security policy](security.md) for security boundaries and limitations.

## Short command and agent choice

`agw` and `agentwisp` use the same commands, container names and monitor state.
Build/install both entrypoints as shown above; a shell alias is not required.

```sh
agw
agw add
agw agent claude
agw agent claude -- --continue
agw agent codex
agw stop
```

`agent` requires an existing workspace with the selected executable installed.
It starts a stopped workspace, runs the agent in `/workspace`, and uses the
configured traffic monitor. It does not install agents, copy host credentials,
transfer conversations between providers, or add permission-bypass flags.
Pass agent-specific arguments after the provider name, optionally following `--`.
Use interactive input for sensitive prompts; argv is visible in process listings.

The default image contains both tools; build it using [`images/agents/Readme.md`](images/agents/Readme.md).
Existing workspace images remain unchanged. Authenticate each agent inside its
container; removal loses container-only auth and session state.

## Multi-workspace interface direction

The recommended direction is a local Web UI backed by a persistent session
service, while retaining the CLI. The current foundations are described in
[Workspaces, agent sessions, and attachments](docs/core-models.md) and
[Workspace service and runtime adapter](docs/runtime-service.md).
Managed sessions and context exchange between workspaces are not implemented yet.

Open [`design/workspace-console/index.html`](design/workspace-console/index.html)
in a browser for the interactive split-view concept. It uses sample data only:
there is no live web API or terminal connection. Try two/four panes, per-pane
workspace selection, focus, search, the attention queue, and diff/access/events.

Until the session service exists, run `agw agent claude` / `agw agent codex`
in separate terminal panes, each from the intended project directory. Separate
panes do not isolate files: use separate project copies for simultaneous writers.
