# Default workspace image, built locally as agentwisp-agents:local.
# Pass reviewed exact CLI versions and a BASE_IMAGE digest for releases.
ARG BASE_IMAGE=docker.io/library/node:22-bookworm
FROM ${BASE_IMAGE}
ARG CODEX_VERSION
ARG CLAUDE_VERSION

USER node
ENV HOME=/home/node \
    NPM_CONFIG_PREFIX=/home/node/.local \
    PATH=/home/node/.local/bin:$PATH \
    CLAUDE_CONFIG_DIR=/home/node/.claude \
    DISABLE_AUTOUPDATER=1

RUN test -n "$CODEX_VERSION" && test -n "$CLAUDE_VERSION" \
    && printf '%s' "$CODEX_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
    && printf '%s' "$CLAUDE_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
    && npm install --global --no-audit --no-fund \
       "@openai/codex@${CODEX_VERSION}" "@anthropic-ai/claude-code@${CLAUDE_VERSION}" \
    && npm cache clean --force \
    && codex --version && claude --version

WORKDIR /workspace
CMD ["bash"]
