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

## Reporting a vulnerability

Do not publish exploitable details in a public issue before a fix is available.
Send the maintainer a private report with the affected revision, reproduction
steps, impact, and suggested mitigation.

Rotate exposed credentials immediately. Never include real secrets in reports,
tests, logs, screenshots, or example commands.
