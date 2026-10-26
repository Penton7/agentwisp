# agw doctor

`agw doctor` is a read-only health and safety check. It never runs git, never
starts containers or proxies, and never changes files. Run it after installing
a new AgentWisp version, before trusting an old workspace, and in scripts:

```sh
agw doctor            # human-readable report
agw doctor --json     # {"schema_version":1,"current_layout":N,"findings":[...]}
agw doctor --strict   # also exit nonzero on warnings (CI, release checks)
```

Exit status is nonzero when any finding is `fail` (or `warn` with `--strict`).
Each finding has a stable `check` ID, a `level` (`ok`, `warn`, `fail`), a
`subject` (`host`, a workspace or a task) and usually a `fix`.

## Checks

| ID | Level | Meaning |
| --- | --- | --- |
| `runtime.available` | fail | Apple `container` cannot list workspaces |
| `git.available` | warn | git missing on the host; `agw task` unavailable |
| `state.permissions` | fail | `~/.agentwisp` contains symlinks or files readable by others |
| `workspace.layout` | warn | Container created by an older (or newer) AgentWisp; lists what recreating adds |
| `workspace.path` | warn | Mounted host path is unknown or missing |
| `workspace.git-exec` | fail / warn | The mounted `.git` has config, hooks or attributes that make host git run programs (`core.fsmonitor`, `core.pager`, `core.hooksPath`, filters, diff/merge drivers, `!` aliases, includes, credential helpers, hooks, ...). `fail` for project workspaces; `warn` for task clones, where it is expected and host git must not run anyway |
| `workspace.git-shared` | ok / warn | Whether a project `.git` (not a task clone) is read-only inside its workspace. `warn`: writable (layout < 3 or `AGENTWISP_WRITABLE=.git`) |
| `workspace.protected` | ok / warn | Layout 3+: protected paths (`.git`, `.idea`, `.vscode`, `.claude`, `.mcp.json`, `.codex`, `.husky`, `.githooks`, `.pre-commit-config.yaml`, `.envrc`) that exist in the project but are writable in the workspace, because they appeared after creation or were opted out. The agent may have written them |
| `monitor.bind` | warn | A running proxy still listens on all interfaces (started by an older agw) |
| `monitor.health` | warn | Monitor is enabled but the proxy does not respond, or its config is unreadable |
| `task.state` | warn | A task record is unreadable or its clone is missing |

The git check is a lenient parser of `.git/config`, `config.worktree`,
`.git/modules/*`, `hooks/` and `info/attributes`. It may report entries git
would ignore; it aims not to miss ones git would honor. If you did not add a
reported entry, do not run git in that project until you have removed it.

## Workspace layouts

A container's layout is fixed at creation (main process, mounts, labels) and is
recorded in the `dev.agentwisp.layout` label. Containers without the label are
layout 1.

| Layout | Changes |
| --- | --- |
| 1 | Interactive bash as the container's main process; no saved logins or agent status |
| 2 | Keep-alive main process, saved agent logins, agent status mount, layout label |
| 3 | Protected project paths (`.git`, IDE, agent and hook configuration, `core.hooksPath` inside the project) mounted read-only and verified from inside the container; `dev.agentwisp.readonly` label |

## For developers

- Changing what `Create` sets up for new containers: bump
  `currentWorkspaceLayout` and add a `workspaceLayoutHistory` entry and a row
  above. A test fails if the history is incomplete.
- Adding a check: add an ID constant, list it in `doctorChecks`, document it in
  the table. A test fails if an ID is not documented.
- Doctor must stay read-only and must treat everything inside mounted
  directories as untrusted.
