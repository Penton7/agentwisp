# Build once, then select this local image through AGENTWISP_IMAGE.
# For reproducible releases, supply a reviewed node image digest as BASE_IMAGE.
ARG BASE_IMAGE=docker.io/library/node:22-bookworm
FROM ${BASE_IMAGE}

# Deliberately required: upgrades must select an explicit Codex version.
ARG CODEX_VERSION
RUN test -n "$CODEX_VERSION" \
    && printf '%s' "$CODEX_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' \
    && npm install --global --ignore-scripts --no-audit --no-fund "@openai/codex@${CODEX_VERSION}" \
    && npm cache clean --force \
    && codex --version

# Credentials are created by the user inside this container, never in the image.
USER node
ENV HOME=/home/node
WORKDIR /workspace
CMD ["bash"]
