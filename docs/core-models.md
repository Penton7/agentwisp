# Workspaces, agent sessions, and attachments

AgentWisp defines three separate objects in
[`model.go`](../internal/agentwisp/model.go). The CLI and TUI inventory use
`Workspace`. Session and attachment models provide a foundation for future
session management; they do not enable background sessions or reconnect today.

## Objects and identifiers

| Object | Identity | Purpose |
| --- | --- | --- |
| `Workspace` | `WorkspaceID`, prefixed with `ws_` | An environment: `Name` is the runtime container name, `Path` is the host project directory, and `Running` is the observed container state. |
| `AgentSession` | `AgentSessionID`, prefixed with `ses_` | One Claude or Codex execution, with a reference to its workspace, a working directory inside the container, and agent state. |
| `Attachment` | `AttachmentID`, prefixed with `att_` | One CLI or web view of an agent execution. Multiple attachments may reference the same session. |

The IDs have distinct Go types. Container names, provider conversation IDs, and
AgentWisp object IDs are not interchangeable. An ID does not grant access.

## Workspace identity

Until a persistent registry exists, a workspace ID is derived deterministically
from the container name and the Apple runtime namespace. Reading inventory again
does not change the ID, and legacy containers are not renamed.

This identity is local to one host. Deleting and recreating a container with the
same name currently reuses its workspace ID. A future registry must preserve
existing IDs during import and distinguish container incarnations before stored
references or access grants rely on them.

## Session metadata and state

Session and attachment IDs are generated randomly. Their constructors allocate
metadata only: they neither start processes nor persist records. The current
`agw agent` command remains a synchronous launch attached to the host terminal.

A new session starts in `unknown`, even if its container is running. The
`starting`, `working`, `needs-input`, and `exited` states require explicit process
or provider observations. An inventory error must not be interpreted as a
stopped container. Agent events and status freshness are not implemented yet.

`NativeSessionID` is an optional provider conversation ID, initially empty. The
model distinguishes resuming a conversation as a new execution with a new
`AgentSessionID` from attaching another view to an existing execution.

`CWD` is an absolute POSIX path inside the container, independent of the host
project path. Validating its format does not grant access or replace checks on
the runtime and mounted directories.

## Attachments

A new attachment has `ReadOnly=true`. This is a default metadata value, not an
implemented authorization mechanism. A future session broker must validate
access and enforce a single input owner. Closing a view should remove only the
attachment; terminating an agent and deleting a workspace are separate actions.

## Current limits and validation

A persistent registry, HTTP API, managed PTYs, reconnect, and context exchange
are not implemented. Read-only [JSON CLI commands](json-inventory.md) expose
workspace inventory. The shared service is described in
[Workspace service and runtime adapter](runtime-service.md).

Validation performed on Linux includes Go tests with the race detector, vet,
builds of both CLI entrypoints, Darwin arm64 cross-builds, and PTY smoke tests
against a fake runtime. Smoke tests cover navigation, deletion, resize,
start/stop/attach, and terminal restoration. These checks do not replace testing
with the actual Apple runtime on macOS.
