# Workspace service and runtime adapter

The CLI and TUI use the same `WorkspaceService`. It resolves a host project path
to an existing container name and handles creation, entry, shell execution,
agent launch, stopping, and removal. Existing commands and the default image
remain unchanged.

## Runtime boundary

[`Runtime`](../internal/agentwisp/runtime.go) defines
`List`, `Exists`, `Running`, `Start`, `Stop`, `Remove`, `Create`, `Addresses`,
`CheckAgent`, and `Launch`.
The `appleRuntime` adapter constructs argument arrays for Apple's `container`
CLI. It preserves batched inspection, sorting, and legacy container discovery.

`RuntimeCreate` creates a detached container whose main process is a keep-alive
loop; `RuntimeLaunch` is always an interactive `exec` into an existing container,
so no user shell or agent is the container's main process.
`RuntimeLaunch` carries structured launch parameters. User prompts and paths are
not interpolated into shell source. The executable check uses a fixed script
with the executable passed separately as `$1`.

## Lifecycle and monitoring

[`WorkspaceService`](../internal/agentwisp/workspace.go) coordinates the runtime
with a separate `Monitor` interface. The monitor prepares the proxy environment
and releases its temporary environment file after the interactive command
returns, including when launch fails.

- Stop terminates the container first, then shuts down its proxy.
- Remove checks existence and running state, then shuts down the proxy before
  removing the container. A proxy shutdown failure prevents container removal.
- Host project files and traffic logs are retained.
- Enter checks current runtime state instead of relying on a stale TUI row.
- The TUI refreshes inventory after a stop attempt and reports failures.
- Shell preserves its exec-only behavior; it does not start stopped containers.

The same lifecycle policy applies to the CLI and TUI.

## Error handling

Callers can distinguish errors with `errors.Is`:

| Error | Evidence |
| --- | --- |
| `ErrWorkspaceAbsent` | A successful inventory query does not contain the selected container. |
| `ErrRuntimeUnavailable` | The CLI is missing, or a typed system connection error is available. |
| `ErrRuntimePermission` | A typed operating system permission error is available. |
| `ErrRuntimeFailed` | Other CLI launch or exit failures. |

The underlying cause is retained through `Unwrap`. Human-readable stderr is not
parsed to guess whether a workspace is absent or the runtime is unavailable or
denied access. A daemon or permission error reported only as a CLI exit and
stderr therefore remains `ErrRuntimeFailed`, with its original diagnostic.
More precise classification requires a verified machine-readable runtime error
contract.

An inventory error never means that a container is absent and never triggers
creation of a replacement container.

## Testing

Service tests inject a fake runtime and monitor without launching subprocesses.
Integration tests using a fake `container` executable are also retained. Tests
cover argument boundaries, error classification, stop/remove ordering, avoiding
mutations after runtime failures, cleanup, and service use by the CLI and manager.

Validation includes gofmt, `go vet ./...`, `go test -race ./...`, both CLI builds
on Linux, Darwin arm64 cross-builds, and PTY smoke tests against a fake runtime.
The smoke tests cover management actions, deletion, resize, and terminal
restoration. The actual Apple runtime still requires validation on macOS.

## Current limits

The service is synchronous. Cross-process locking, deadlines, and races between
existence checks and mutations are not addressed yet. A session broker, Web API,
and persistent registry are not implemented. Interactive processes remain
attached to the host terminal; this interface is not a web-terminal transport.

Read-only JSON inventory and inspection use this service. See the
[JSON command reference](json-inventory.md) for the versioned output contract.
