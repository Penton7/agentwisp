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
- `internal/agentwisp/task.go` implements isolated task clones and bundle-based fetch.
- `internal/agentwisp/status.go` installs agent status hooks, reads untrusted status files, and implements `agw watch`.
- `internal/agentwisp/doctor.go` implements read-only `agw doctor` checks and the workspace layout history.
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
- New containers run a keep-alive main process; shells and agents are always `exec` sessions.
- Create/start/stop/remove hold the per-workspace lifecycle lock, never an interactive session. Lock order: lifecycle, then monitor.
- Changing what new containers get at creation (main process, mounts, labels) requires bumping `currentWorkspaceLayout` and documenting it in `workspaceLayoutHistory` and `docs/doctor.md`; tests enforce this.
- New doctor checks need a stable ID in `doctorChecks` and a row in `docs/doctor.md`. Doctor stays read-only and never runs git.
- Never run host git inside a task clone; host git runs only in the main repository with hooks and fsmonitor disabled.
- `agentwisp help` documents commands, aliases, and TUI controls.
- `list`, `ls`, and `.` remain manager aliases unless intentionally deprecated.
- `list --json` and `inspect <id> --json` are read-only; JSON stdout is separate from errors on stderr. Never expose raw inspect or monitor credentials.
- Enter opens a shell (starting a stopped workspace); `C`/`X` run Claude/Codex; `F` fetches a task. After the session the manager reopens on the same row.
- `S` stops the selected running workspace and disables its traffic monitor proxy
  without closing the TUI.
- `R` refreshes container state and preserves selection by name.
- `D` requests deletion; `Y` confirms and any other key cancels.
- Deletion keeps host project files and traffic logs, and stops the proxy first.
- Rows show human labels (project or `repo / task`) grouped in attention sections (needs you, ready, working, no agent, stopped), alphabetical inside a section; selection is kept by container name.
- The detail panel describes the selected workspace: task/project, agent sessions, proxy and allowlist, saved logins, layout.
- At 100+ columns use the list + detail pane layout; narrower terminals get a two-line summary. `? help` and `q quit` always stay in the key bar.
- Rendering (`tui_view.go`) is pure; file reads and proxy probes happen only in the loop.
- Colors come only from the roles in `tui_style.go` and the palettes in `tui_theme.go`; `TestPaletteContrast` must pass for both themes. Color only what needs attention, keep meaning visible without color, and honor `NO_COLOR`/`TERM=dumb`.

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
- Only the glyphs in `trustedGlyphs` (`tui_style.go`) may be printed as
  non-ASCII; each is one cell and has an ASCII fallback. Everything else
  non-ASCII is escaped. Rune count is not sufficient for other Unicode text.

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
- Enter, `C`, `X`, `F`, `S`, `D`, `R`, `?`, `Q`, Escape, arrows, and `J/K`;
- return to the manager after a shell/agent exits;
- list scrolling with more workspaces than terminal rows;
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
