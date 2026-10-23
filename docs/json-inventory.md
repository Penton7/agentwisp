# JSON workspace inventory

AgentWisp provides read-only JSON commands for scripts and integrations. Both
`agw` and `agentwisp` support the same options.

```sh
agw list --json
agw inspect <workspace-id> --json
```

Use the full `id` returned by the list command. Inspection accepts an exact
AgentWisp workspace ID, not a container name, path, or ID prefix.
`agw inspect --json <workspace-id>` is also supported.

`agw ls --json` and `agw . --json` are list aliases. Without `--json`, `agw`,
`agw list`, `agw ls`, and `agw .` keep their existing workspace manager behavior.
Inspection currently requires `--json`. Unknown flags, duplicate flags, and
extra arguments are rejected.

## Response format

Every successful response contains one JSON object followed by a newline.
`schema_version` is currently `1`. Consumers should check this version and
ignore unrecognized fields within a supported version. Incompatible changes
require a new schema version.

A list response has this shape (the ID below is abbreviated for readability):

```json
{
  "schema_version": 1,
  "workspaces": [
    {
      "id": "ws_…",
      "container_name": "agentwisp-work-example-0123456789abcdef",
      "host_path": "/Users/example/projects/example",
      "container_state": "running"
    }
  ]
}
```

An empty inventory returns `{"schema_version":1,"workspaces":[]}`. The array is
never `null`. Workspaces are ordered by running state first, then container name.

Inspection returns the same workspace fields in a singular envelope:

```json
{
  "schema_version": 1,
  "workspace": {
    "id": "ws_…",
    "container_name": "codex-work-example",
    "host_path": null,
    "container_state": "stopped"
  }
}
```

| Field | Meaning |
| --- | --- |
| `id` | Host-local AgentWisp workspace identity. Treat it as opaque. |
| `container_name` | Existing runtime container name, including legacy names. |
| `host_path` | Host project path from runtime metadata, or `null` if unavailable. |
| `container_state` | `running` or `stopped`, based on runtime inventory. This is not agent activity. |

Names and paths are untrusted metadata. JSON encoding preserves their contents
and escapes control characters; consumers must not interpolate them into shell
commands or render them as HTML. The response intentionally includes host paths,
which may disclose local directory names.

## Errors and output streams

Successful commands exit with status `0` and write only JSON to stdout. Invalid
arguments, missing or ambiguous IDs, and runtime failures exit with status `1`;
the CLI writes a diagnostic to stderr. No JSON error envelope is emitted.

Argument validation and runtime queries finish before writing a response, so
those failures leave stdout empty. A failure while writing stdout is also
reported, but may leave a partial response if the destination accepted some
bytes before failing.

Unavailable runtime inventory is an error, not an empty list or a stopped
workspace. An unknown ID does not create a workspace.

## Read-only behavior and limitations

These commands only read container inventory. They do not create, start, stop,
remove, or attach to containers and do not start the monitor proxy. Responses
use an explicit field allowlist, excluding raw inspect records, environment
variables, unrelated labels, credentials, monitor configuration, and agent
prompts or output.

Inventory is observed through multiple runtime queries, not an atomic snapshot.
Containers may change between queries; an inconsistent inspection can fail
rather than returning a partial list. Workspace IDs currently derive from the
container name: deleting and recreating a container with the same name reuses
its ID. IDs are not authorization tokens or container incarnation identifiers.
See [workspace identity](core-models.md#workspace-identity).
