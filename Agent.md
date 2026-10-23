# AgentWisp Development Guide

This file defines the working rules for developers and coding agents modifying
this repository.

## Project goal

AgentWisp is a small macOS CLI for managing isolated development workspaces with
Apple's `container` command.

The intended workflow is:

1. Run `agentwisp add` inside a project directory.
2. AgentWisp creates or starts a container and mounts that directory at
   `/workspace`.
3. Run `agentwisp` from anywhere to open the workspace manager.
4. Start, attach to, stop, or inspect registered workspaces from the TUI.

Keep the tool fast, predictable, and easy to distribute as one binary.

## Current architecture

- `cmd/agentwisp/main.go` and `cmd/agw/main.go` are process entrypoints for the same CLI.
- `internal/agentwisp/agent.go` validates provider selection and starts an installed agent in a workspace.
- `internal/agentwisp/cli.go` parses commands and dispatches workflows.
- `internal/agentwisp/config.go` contains container naming and discovery constants.
- `internal/agentwisp/model.go` separates workspace identity, agent execution, and client attachment metadata.
- `internal/agentwisp/inventory.go` exposes the versioned, allowlisted JSON inventory and exact workspace ID lookup.
- `internal/agentwisp/command.go` wraps external command execution.
- `internal/agentwisp/runtime.go` defines the runtime contract, typed failures, and Apple command adapter.
- `internal/agentwisp/container.go` implements Apple inventory and parses inspect output.
- `internal/agentwisp/workspace.go` provides the shared workspace service used by CLI and TUI.
- `internal/agentwisp/service_test.go` checks lifecycle policy using an injected fake runtime and monitor.
- `internal/agentwisp/terminal.go` owns terminal mode, key parsing, and terminal width detection.
- `internal/agentwisp/tui.go` renders and runs the workspace manager UI.
- `internal/agentwisp/main_test.go` contains parser, layout, keyboard, and command help tests.
- `security.md` defines dependency and container security policy.
- `go.mod` intentionally has no third-party dependencies.

Managed containers use:

- name prefix: `agentwisp-work-`;
- label `dev.agentwisp.managed=true`;
- label `dev.agentwisp.workspace=<host-path>`;
- host project mounted at `/workspace`;
- working directory `/workspace`;
- default image `agentwisp-agents:local`, built from `images/agents`, overridden by `AGENTWISP_IMAGE` for new containers; existing containers retain their image;
- new names include a canonical-path hash; legacy containers are matched by path.

Older `codex-work-*` containers and `dev.codexbox.*` labels must remain
discoverable. Containers without labels must remain discoverable through their
`/workspace` bind mount.

## Required behavior

- `agentwisp` and `agw` open the same TUI workspace manager.
- `agw agent claude|codex [-- args...]` uses an existing workspace and preserves argv boundaries.
- Never automatically pass agent permission-bypass flags or mount host credentials.
- `agentwisp add`, `start`, and `run` create/start the current workspace and enter
  it.
- `agentwisp shell`, `stop`, and `rm` operate on the current directory workspace.
- `agentwisp help` documents commands, aliases, and TUI controls.
- `list`, `ls`, and `.` remain manager aliases unless intentionally deprecated.
- `list --json` and `inspect <id> --json` are read-only; JSON stdout is separate from errors on stderr. Never expose raw inspect or monitor credentials.
- Enter starts a stopped workspace or attaches to a running one.
- `S` stops the selected running workspace and disables its traffic monitor proxy
  without closing the TUI.
- `R` refreshes container state and preserves selection by name.
- `D` requests deletion; `Y` confirms and any other key cancels.
- Deletion keeps host project files and traffic logs, and stops the proxy first.
- If the traffic monitor is configured for the selected workspace, the TUI shows
  proxy health, PID, port, private LAN policy, and log path.

## Terminal and TUI invariants

These rules are mandatory because violating them has already caused broken
terminal output:

- Do not use `stty raw`. It disables output processing such as `ONLCR`, causing
  newline output to render as a diagonal staircase.
- For immediate key input, use `stty -icanon -echo min 1 time 0` and preserve the
  original terminal state.
- Always restore terminal state, cursor visibility, and alternate screen on every
  exit path.
- Keep the final terminal column unused to avoid automatic wrapping.
- ANSI escape sequences must not count toward visible column width.
- Header and data rows must have identical visible widths.
- Long values must be truncated, never wrapped inside a table row.
- Handle `SIGWINCH` and recompute the terminal width after resize.
- Keep a narrow-terminal fallback.
- Do not introduce Unicode glyphs into width-sensitive columns without adding
  proper display-cell width handling. Rune count is not sufficient for all
  Unicode characters.

## Implementation guidelines

- Prefer small functions with explicit inputs over global mutable state.
- Preserve the selected workspace by container name after refresh or sorting.
- Return errors instead of silently discarding command failures.
- Do not parse human-formatted command output when JSON or quiet identifiers are
  available.
- Treat `container inspect` output as untrusted data.
- Never execute values obtained from labels, paths, or inspect output.
- Preserve compatibility with pre-rename Codexbox containers.
- Do not modify unrelated user files or replace the existing binary during tests.

## Dependency policy

The project currently uses only the Go standard library. Do not add a module for:

- terminal colors;
- table rendering;
- command parsing;
- small string or collection helpers.

Before adding any dependency, follow every requirement in `security.md`. A new
dependency requires a clear technical justification, source review, transitive
dependency review, and `govulncheck`.

## Verification

Run before handing off a change:

```sh
gofmt -w cmd/agentwisp cmd/agw internal/agentwisp
go vet ./...
go test ./...
go build -o /tmp/agentwisp-check ./cmd/agentwisp
go build -o /tmp/agw-check ./cmd/agw
```

After dependency changes also run:

```sh
go mod verify
govulncheck ./...
```

For TUI changes, manually verify at least:

- wide and narrow terminal windows;
- terminal resize while the TUI is open;
- running and stopped workspaces;
- long workspace names and host paths;
- Enter, `S`, `R`, `Q`, Escape, arrows, and `J/K`;
- terminal restoration after normal exit, error, and `Ctrl-C`.

## Installation during development

Build locally first:

```sh
go build -o agentwisp ./cmd/agentwisp
./agentwisp help
./agentwisp
```

Install only after verification:

```sh
sudo install -m 755 ./agentwisp /usr/local/bin/agentwisp
rehash
```

If the installed binary behaves differently, compare paths and checksums:

```sh
type -a agentwisp
shasum -a 256 ./agentwisp /usr/local/bin/agentwisp
```

## Good next improvements

Potential features should be implemented incrementally:

- configurable agent command, for example `bash`, `codex`, or `opencode`;
- persisted workspace metadata under an AgentWisp state directory;
- optional per-workspace traffic visibility;
- search and vertical scrolling for large workspace lists;
- rename or display a friendly workspace name;
- validated image presets and reproducible digest-pinned releases;
- persisted project configuration;
- sorting options;
- better non-interactive output for scripts.

Do not combine unrelated features into one large refactor.
