# Claude Code + Codex image

Build from the AgentWisp repository root. Supply reviewed, exact CLI versions
(both values are deliberately required):

```sh
: "${CODEX_VERSION:?Set CODEX_VERSION to a reviewed X.Y.Z version}"
: "${CLAUDE_VERSION:?Set CLAUDE_VERSION to a reviewed X.Y.Z version}"
container build \
  --build-arg CODEX_VERSION="$CODEX_VERSION" \
  --build-arg CLAUDE_VERSION="$CLAUDE_VERSION" \
  -t agentwisp-agents:local images/agents
```

For release builds also supply `--build-arg BASE_IMAGE=<reviewed-image@sha256:digest>`.
The default image for new workspaces is `agentwisp-agents:local`. Build it once
before creating a workspace; AgentWisp does not build it automatically.
Rebuilding this tag affects only subsequently created containers. Existing
containers keep their image and data, including after stop/start.

In a project without an existing workspace:

```sh
agw add
# Inside the shell: sign in, then exit back to the host.
claude
codex login --device-auth
exit

# From the host project directory:
agw agent claude
agw agent claude -- --continue
agw agent codex
```

Agent commands require an existing workspace, start it if stopped, and launch
the agent in `/workspace`. Missing executables produce an error instead of
installing software implicitly. Additional arguments are passed as distinct
arguments, including when the traffic monitor is enabled. Prompts passed as
arguments remain visible in OS process listings; use interactive input for
sensitive text. Agent stderr is shown as emitted by the agent.

The image uses `node`, installs both tools in that user's home, and contains
no login state. `CLAUDE_CONFIG_DIR` keeps Claude state in `/home/node/.claude`.
The home persists across stop/start, but deletion/recreation loses it.
Changing `AGENTWISP_IMAGE` never upgrades an existing container.
To use a different image for a new workspace, run
`AGENTWISP_IMAGE=<image:tag> agw add`.

Claude's documented pinned npm installation is used for this build recipe;
auto-update is disabled. Package installation scripts run inside the build as
`node`; this is not a package security audit. Both CLI version checks run during
the build. The recipe still needs an Apple-container arm64 build/smoke test.
Host credentials, host home directories and SSH agents are not mounted.
No permission-bypass flags are added. Each agent keeps its own context and auth;
switching executables does not transfer a conversation.

References: [Claude development containers](https://code.claude.com/docs/en/devcontainer),
[Claude installation](https://code.claude.com/docs/en/setup),
[Codex authentication](https://learn.chatgpt.com/docs/auth).
