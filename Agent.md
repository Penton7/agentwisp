# Codexbox Development Guide

This file defines the working rules for developers and coding agents modifying
this repository.

## Project goal

Codexbox is a small macOS CLI for managing isolated development workspaces with
Apple's `container` command.

The intended workflow is:

1. Run `codexbox add` inside a project directory.
2. Codexbox creates or starts a container and mounts that directory at
   `/workspace`.
3. Run `codexbox` from anywhere to open the workspace manager.
4. Start, attach to, stop, or inspect registered workspaces from the TUI.

Keep the tool fast, predictable, and easy to distribute as one binary.

## Current architecture

- `main.go` contains the CLI, container integration, TUI, and terminal handling.
- `main_test.go` contains parser, layout, keyboard, and command help tests.
- `security.md` defines dependency and container security policy.
- `go.mod` intentionally has no third-party dependencies.

Managed containers use:

- name prefix: `codex-work-`;
- label `dev.codexbox.managed=true`;
- label `dev.codexbox.workspace=<host-path>`;
- host project mounted at `/workspace`;
- working directory `/workspace`;
- image `docker.io/library/node:22-bookworm`.

Older containers without labels must remain discoverable through their
`/workspace` bind mount.

## Required behavior

- `codexbox` opens the TUI workspace manager.
- `codexbox add`, `start`, and `run` create/start the current workspace and enter
  it.
- `codexbox shell`, `stop`, and `rm` operate on the current directory workspace.
- `codexbox help` documents commands, aliases, and TUI controls.
- `list`, `ls`, and `.` remain manager aliases unless intentionally deprecated.
- Enter starts a stopped workspace or attaches to a running one.
- `S` stops the selected running workspace without closing the TUI.
- `R` refreshes container state.

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
- Preserve compatibility with previously created Codexbox containers.
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
gofmt -w main.go main_test.go
go vet ./...
go test ./...
go build -o codexbox .
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
go build -o codexbox .
./codexbox help
./codexbox
```

Install only after verification:

```sh
sudo install -m 755 ./codexbox /usr/local/bin/codexbox
rehash
```

If the installed binary behaves differently, compare paths and checksums:

```sh
type -a codexbox
shasum -a 256 ./codexbox /usr/local/bin/codexbox
```

## Good next improvements

Potential features should be implemented incrementally:

- remove a workspace from the TUI with confirmation;
- rename or display a friendly workspace name;
- configurable container image;
- persisted project configuration;
- search/filter for large workspace lists;
- sorting options;
- image digest pinning;
- better non-interactive output for scripts.

Do not combine unrelated features into one large refactor.
