#!/usr/bin/env bash
# Copy Matt Pocock's engineering skills into the agent's personal skills dir,
# so they load in every session regardless of cwd. Public repo, pinned ref.
set -euo pipefail
source /etc/nightshift/nightshift.env

AGENT_HOME="/home/${AGENT_USER}"
DEST="${AGENT_HOME}/.claude/skills"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

git clone --depth 1 https://github.com/mattpocock/skills "${TMP}/skills"
if [ "${SKILLS_REPO_REF}" != "main" ]; then
  git -C "${TMP}/skills" fetch --depth 1 origin "${SKILLS_REPO_REF}"
  git -C "${TMP}/skills" checkout "${SKILLS_REPO_REF}"
fi

mkdir -p "${DEST}"
cp -r "${TMP}/skills/skills/engineering/." "${DEST}/"
chown -R "${AGENT_USER}:${AGENT_USER}" "${AGENT_HOME}/.claude"
echo "[nightshift] skills installed -> ${DEST}"
