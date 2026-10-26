# Security Policy

## Dependency policy

`agentwisp` currently uses only the Go standard library. Keep it dependency-free
unless an external module is necessary and has been explicitly reviewed.

Before adding or updating any Go module:

1. Document why the standard library or existing code is insufficient.
2. Verify the exact module path to avoid typosquatting and repository substitution.
3. Review its source, maintainers, license, releases, open security issues, and
   maintenance activity.
4. Prefer a tagged release. Avoid arbitrary commits and unmaintained forks.
5. Inspect all transitive modules:

   ```sh
   go mod graph
   go list -m -json all
   ```

6. Scan the complete dependency graph:

   ```sh
   go install golang.org/x/vuln/cmd/govulncheck@latest
   govulncheck ./...
   ```

7. Review every change in `go.mod` and `go.sum`. Any unexplained indirect module
   blocks the change.
8. Run tests and build from a clean module cache when practical.

Do not add a module solely for colors, terminal tables, formatting, or a small
helper that can be implemented safely with the standard library.

A clean vulnerability scan is required, but it does not prove that a dependency
is trustworthy. Source and maintainer review are still required.

## Required checks

Run before release and after every dependency change:

```sh
go mod verify
go vet ./...
go test ./...
govulncheck ./...
```

If the project is expected to remain dependency-free, verify that `go.mod`
contains only the module name and Go version, and that no unexpected `go.sum`
was introduced.

## Container safety

- The TUI displays `agentwisp-work-` containers and legacy `codex-work-`
  containers retained for migration compatibility.
- A workspace mount gives the container access to host files in that directory.
  Do not mount sensitive parent directories or open untrusted projects.
- Container images are dependencies. For security-sensitive or reproducible use,
  pin a reviewed image digest instead of relying only on a mutable tag.
- Do not pass host secrets, credentials, SSH agents, or extra mounts by default.
- Treat `container inspect` output as untrusted data and never execute it.

## Traffic monitor safety

The experimental traffic monitor must remain metadata-only by default.

Allowed default log fields:

- timestamp;
- workspace/container name;
- scheme;
- destination host;
- destination port;
- HTTP method or CONNECT;
- byte counts;
- allow/deny result;
- connection error text.

Do not log by default:

- Authorization headers;
- cookies;
- request or response bodies;
- full query strings;
- API keys;
- bearer tokens;
- SSH keys;
- package registry tokens.

The monitor should be opt-in per workspace. Treat traffic logs as sensitive local
data and store them with user-only file permissions.

The proxy must require a per-workspace token. Do not run it as an unauthenticated
open proxy.

Do not pass the token through process arguments. Tokens belong in user-readable
state files only.

The proxy must block loopback, unspecified, multicast, and link-local
destinations by default. A workspace must not be able to use the monitor proxy to
reach services bound to the host laptop's `127.0.0.1` or `::1`.

The proxy must also block private LAN ranges by default, including `10.0.0.0/8`,
`172.16.0.0/12`, `192.168.0.0/16`, and `fc00::/7`. Allowing private LAN access
requires explicit user opt-in with `agentwisp monitor on --allow-private`.

HTTP `CONNECT` must be restricted to expected HTTPS tunneling use by default.
Do not allow arbitrary TCP tunneling without a separate explicit opt-in.

Proxy lifecycle should use authenticated health/shutdown requests. Do not rely
only on PID checks because PIDs can be reused by unrelated processes.

The proxy binds to the host's address on the workspace container network,
selected by matching the container's reported address against the host's own
interfaces. Container-reported addresses are never bound or dialed directly.
`0.0.0.0` is available only as an explicit `AGENTWISP_PROXY_LISTEN_HOST` opt-in.

Do not add transparent TLS interception or custom CA installation without a
separate explicit design review and user confirmation flow.

## Current security scope

The current source review and remaining risks are recorded separately in
[`docs/security-review-2026-09-08.md`](docs/security-review-2026-09-08.md).
The proxy is an observation tool, not an enforced network sandbox. Writable
workspace mounts include project secrets and Git metadata. Do not describe
container isolation as protection against edits to the mounted project.

Workspace deletion must confirm its data-loss scope, validate its target,
stop the proxy before deleting the container, and preserve host source files.
Proxy shutdown failures must retain state for retry. Runtime credentials and
logs require separate retention and revocation decisions.

## Vulnerability reports

Do not publish exploitable details in a public issue before a fix is available.
Send the maintainer a private report with the affected revision, reproduction
steps, impact, and suggested mitigation.

Rotate exposed credentials immediately. Never include real secrets in reports,
tests, logs, screenshots, or example commands.

## Git directories and isolated tasks

A git directory writable by workspace code is untrusted for host git commands:
`core.fsmonitor`, `core.pager`, aliases, filters, and hooks in it can run
programs on the host. Project workspaces (layout 3) mount `.git`, IDE, agent
and hook-manager configuration and an in-project `core.hooksPath` read-only,
verify that from inside the container, and refuse the workspace otherwise.
This holds while the container user cannot remount (not root, no
`CAP_SYS_ADMIN`). Paths created after the workspace are not covered;
`agw doctor` reports them. `agw task` never mounts the main repository's `.git`,
uses `--no-hardlinks` so the clone shares no object inodes with the main
repository, and returns results only as a git bundle produced inside the
container. Host git commands run only in the main repository, with
`core.hooksPath=/dev/null` and `core.fsmonitor=false`.

## Agent state mounted from the host

Per-workspace host directories hold agent logins (`home/`) and agent status
(`status/`). They are 0700, separate per workspace, and never contain the
monitor configuration or proxy token. Status files are written inside the
container: the host accepts only regular files (no symlinks), at most 64 files
of 4 KiB, allowlisted states, and uses them for display only. Notification text
is built from sanitized workspace names and allowlisted states and passed to
`osascript` as argv.
