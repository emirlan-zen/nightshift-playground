#!/usr/bin/env bash
# Agent-scoped Codex CLI wrapper. Installed as /usr/local/bin/codex; the real
# binary lives at /usr/local/bin/codex-bin (install.sh deletes the shim npm
# drops in /usr/bin so PATH can't bypass this wrapper).
#
# Codex is optional and uses whichever account the operator authenticates on
# this box. The wrapper fails closed outside the playground workspace.
#
# Identity model: cached OAuth credentials at ~/.codex/auth.json (0600; file
# store on this headless box). No --account flag — the account pin is the auth
# ritual (SETUP.md: sign in ONLY as the operator's ChatGPT account) plus the
# CLAUDE.md rule that agents never touch codex's auth (~/.codex/).
set -euo pipefail
real=/usr/local/bin/codex-bin

ws="${HOME}/workspace"; agent=""
case "${PWD}/" in "${ws}"/*) agent="${PWD#${ws}/}"; agent="${agent%%/*}";; esac
if [ "$agent" != "playground" ] || [ ! -f "/etc/nightshift/secrets/playground.env" ]; then
  echo "codex: refusing — run inside ~/workspace/playground/ (cwd=${PWD})" >&2
  exit 2
fi

exec "$real" "$@"
