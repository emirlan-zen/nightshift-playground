#!/usr/bin/env bash
# Runs once on first boot (as root via cloud-init). Idempotent where practical.
# System setup only - NO secrets, NO private repos. Those are manual post-steps.
set -euo pipefail

source /etc/nightshift/nightshift.env
# boxes provisioned before a var was added to cloud-init's nightshift.env lack
# it — default to the terraform variable's default so reruns don't die unbound
TERRAFORM_VERSION="${TERRAFORM_VERSION:-1.9.8}"
GO_VERSION="${GO_VERSION:-1.26.4}" # >= 1.25 for modernc.org/sqlite (ADR-0006)
REPO=/opt/nightshift
AGENT_HOME="/home/${AGENT_USER}"
log() { echo "[nightshift] $*"; }

# ---------------------------------------------------------------- Docker
log "installing docker"
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --batch --yes --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update -y
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin \
  build-essential make ripgrep tmux
usermod -aG docker "${AGENT_USER}"

# ---------------------------------------------------------------- Go (pinned)
log "installing go ${GO_VERSION}"
ARCH=$(dpkg --print-architecture)   # amd64 / arm64
curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" -o /tmp/go.tgz
rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' > /etc/profile.d/go.sh

# ---------------------------------------------------------------- Node + Claude Code
log "installing node ${NODE_MAJOR} + claude code"
curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash -
apt-get install -y nodejs
npm install -g @anthropic-ai/claude-code

# ---------------------------------------------------------------- playground toolchain
log "installing pnpm + terraform"
npm install -g pnpm wrangler
apt-get install -y unzip
curl -fsSL "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${ARCH}.zip" -o /tmp/terraform.zip
unzip -o /tmp/terraform.zip -d /usr/local/bin terraform && chmod +x /usr/local/bin/terraform

# ---------------------------------------------------------------- GitHub CLI
log "installing gh"
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg 2>/dev/null
chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
  > /etc/apt/sources.list.d/github-cli.list
apt-get update -y
apt-get install -y gh

# ---------------------------------------------------------------- Codex CLI (optional after authentication)
# Installed via npm (pins nothing; upgrade =
# `rm /usr/local/bin/codex-bin` + rerun). npm's shim lands in /usr/bin — we
# re-point it to codex-bin and delete the shim so PATH always resolves to the
# wrapper. Auth is a one-time manual step (SETUP.md).
if [ ! -x /usr/local/bin/codex-bin ]; then
  log "installing codex cli"
  npm install -g @openai/codex
  # resolve the shim via npm's prefix, NOT `command -v` — on an upgrade rerun
  # the wrapper already sits at /usr/local/bin/codex and would win the PATH
  # race, turning codex-bin into a self-referencing symlink
  ln -sfn "$(readlink -f "$(npm prefix -g)/bin/codex")" /usr/local/bin/codex-bin
  rm -f "$(npm prefix -g)/bin/codex"   # PATH must resolve to the wrapper, never the raw shim
fi
# workspace-scoped wrapper (fail-closed outside ~/workspace/playground/)
install -m0755 "${REPO}/files/codex/codex-wrapper.sh" /usr/local/bin/codex
# default model for every call unless the caller overrides; seed-only (operator's
# ~/.codex/config.toml is theirs after first write — never clobber on rerun).
# gpt-5.6-luna = the cheap/fast 5.6 tier: right for delegate grunt work, and
# the codex-auth-probe's default. Scheduler-minted codex jobs (review, ADR-0018)
# pass -m gpt-5.6-sol explicitly and never rely on this default.
if [ ! -f "${AGENT_HOME}/.codex/config.toml" ]; then
  sudo -u "${AGENT_USER}" mkdir -p "${AGENT_HOME}/.codex"
  printf 'model = "gpt-5.6-luna"\n' | sudo -u "${AGENT_USER}" tee "${AGENT_HOME}/.codex/config.toml" >/dev/null
fi

# gh/git use the token in /etc/nightshift/secrets/playground.env.
# `set -a; . "$f"; set +a` — NEVER
# `export $(grep … | xargs)`, which word-splits + glob-expands token values.
PROFILE="${AGENT_HOME}/.profile"
touch "${PROFILE}"; chown "${AGENT_USER}:${AGENT_USER}" "${PROFILE}"
# scrub the old word-splitting loader a pre-2026-07-06 install may have appended
sed -i '/export \$(grep -v "^#" "\$f" | xargs)/d' "${PROFILE}"
grep -qF '/etc/nightshift/secrets/playground.env' "${PROFILE}" || \
  echo 'f=/etc/nightshift/secrets/playground.env; if [ -f "$f" ]; then set -a; . "$f"; set +a; fi' >> "${PROFILE}"

# ---------------------------------------------------------------- git: token credential helper + playground identity
log "configuring git"
[ -x /usr/local/bin/yq ] || { wget -qO /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/latest/download/yq_linux_${ARCH}"; chmod +x /usr/local/bin/yq; }
GC="${AGENT_HOME}/.gitconfig"
git config --file "${GC}" credential."https://github.com".helper '!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f'
for company in $(yq '.companies | keys | .[]' "${REPO}/bootstrap/repos.yaml"); do
  cn=$(yq ".companies.${company}.git_user_name"  "${REPO}/bootstrap/repos.yaml")
  ce=$(yq ".companies.${company}.git_user_email" "${REPO}/bootstrap/repos.yaml")
  printf '[user]\n\tname = %s\n\temail = %s\n' "${cn}" "${ce}" > "${AGENT_HOME}/.gitconfig-${company}"
  git config --file "${GC}" includeIf."gitdir:${AGENT_HOME}/workspace/${company}/".path "${AGENT_HOME}/.gitconfig-${company}"
done
chown "${AGENT_USER}:${AGENT_USER}" "${GC}" "${AGENT_HOME}"/.gitconfig-*

# ---------------------------------------------------------------- Workspace skeleton + CLAUDE.md maps
log "creating workspace skeleton"
sudo -u "${AGENT_USER}" mkdir -p "${AGENT_HOME}/workspace" "${AGENT_HOME}/.claude/skills"
# global rules (every session) + workspace root map
install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 "${REPO}/workspace/global.CLAUDE.md"    "${AGENT_HOME}/.claude/CLAUDE.md"
install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 "${REPO}/workspace/workspace.CLAUDE.md" "${AGENT_HOME}/workspace/CLAUDE.md"
# house visual style, referenced from the workspace CLAUDE.md + night prompts
install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 "${REPO}/workspace/DESIGN.md"           "${AGENT_HOME}/workspace/DESIGN.md"
# night-run banner art-plate style (collage; scoped exception to DESIGN.md), used by the contract + sweep prompts
install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 "${REPO}/workspace/COLLAGE.md"          "${AGENT_HOME}/workspace/COLLAGE.md"
sudo -u "${AGENT_USER}" mkdir -p "${AGENT_HOME}/workspace/playground"
install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 \
  "${REPO}/workspace/playground.CLAUDE.md" "${AGENT_HOME}/workspace/playground/CLAUDE.md"

# ---------------------------------------------------------------- Claude settings (max shell perms)
log "installing claude settings (bypassPermissions)"
install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 \
  "${REPO}/files/claude/settings.json" "${AGENT_HOME}/.claude/settings.json"

# ---------------------------------------------------------------- Skills (public, pinned)
log "fetching mattpocock engineering skills @ ${SKILLS_REPO_REF}"
bash "${REPO}/bootstrap/fetch-skills.sh"

# ---------------------------------------------------------------- Helper commands on PATH
ln -sf "${REPO}/bootstrap/clone-repos.sh" /usr/local/bin/nightshift-clone-repos
ln -sf "${REPO}/bootstrap/post-auth.sh" /usr/local/bin/nightshift-post-auth
chmod +x "${REPO}/bootstrap/clone-repos.sh" "${REPO}/bootstrap/fetch-skills.sh" "${REPO}/bootstrap/post-auth.sh"

# ---------------------------------------------------------------- Traefik
log "starting traefik"
install -d -o "${AGENT_USER}" -g "${AGENT_USER}" "${AGENT_HOME}/traefik"
cp "${REPO}/files/traefik/docker-compose.yml" "${AGENT_HOME}/traefik/"
sed "s|__ACME_EMAIL__|${ACME_EMAIL}|g" "${REPO}/files/traefik/traefik.yml" > "${AGENT_HOME}/traefik/traefik.yml"
cp "${REPO}/files/traefik/docker-proxy.conf" "${AGENT_HOME}/traefik/"
install -d "${AGENT_HOME}/traefik/dynamic"
sed "s|__PREVIEW_DOMAIN__|${PREVIEW_DOMAIN}|g" "${REPO}/files/traefik/dynamic/control.yml" > "${AGENT_HOME}/traefik/dynamic/control.yml"
# certs/ holds the origin cert for the proxied control host. We self-sign it
# (Cloudflare is set to SSL mode "Full", which accepts any origin cert) — avoids
# needing the CF Origin-CA key. Regenerated only if absent.
install -d -o "${AGENT_USER}" -g "${AGENT_USER}" "${AGENT_HOME}/traefik/certs"
CTRL_HOST="control.${PREVIEW_DOMAIN}"
CTRL_CRT="${AGENT_HOME}/traefik/certs/${CTRL_HOST}"
if [ ! -f "${CTRL_CRT}.pem" ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
    -keyout "${CTRL_CRT}.key" -out "${CTRL_CRT}.pem" \
    -subj "/CN=${CTRL_HOST}" \
    -addext "subjectAltName=DNS:${CTRL_HOST}"
fi
chown -R "${AGENT_USER}:${AGENT_USER}" "${AGENT_HOME}/traefik"
docker network create web || true
# TLS-ALPN-01 certs: no token needed. Start it now.
( cd "${AGENT_HOME}/traefik" && docker compose up -d )
log "traefik up"

# ---------------------------------------------------------------- Monitoring (Beszel + Dozzle)
log "staging monitoring stack"
install -d -o "${AGENT_USER}" -g "${AGENT_USER}" "${AGENT_HOME}/monitoring"
sed "s|__PREVIEW_DOMAIN__|${PREVIEW_DOMAIN}|g" "${REPO}/files/monitoring/docker-compose.yml" > "${AGENT_HOME}/monitoring/docker-compose.yml"
chown -R "${AGENT_USER}:${AGENT_USER}" "${AGENT_HOME}/monitoring"
# Dozzle needs a simple-auth users file; generate one if missing (password to stdout).
if [ ! -f /etc/nightshift/secrets/dozzle-users.yml ]; then
  DZPW=$(openssl rand -base64 12)
  docker run --rm amir20/dozzle:latest generate ops --password "${DZPW}" --name ops --email ops@nightshift.local \
    > /etc/nightshift/secrets/dozzle-users.yml 2>/dev/null
  chmod 600 /etc/nightshift/secrets/dozzle-users.yml; chown "${AGENT_USER}:${AGENT_USER}" /etc/nightshift/secrets/dozzle-users.yml
  log "dozzle login -> user: ops  password: ${DZPW}"
fi
( cd "${AGENT_HOME}/monitoring" && docker compose up -d )
log "monitoring up (status.* = beszel, logs.* = dozzle; link beszel agent per SETUP.md)"

# ---------------------------------------------------------------- systemd: remote-control template
# One playground instance or @playground__<slot> for extra named sessions.
# Enabled in post-auth.sh (after
# `claude auth login`), not here. ExecStart is the launcher below.
log "installing remote-control session launcher + template unit"
install -m0755 "${REPO}/files/control/claude-rc-launch" /usr/local/bin/claude-rc-launch
sed "s|__AGENT_USER__|${AGENT_USER}|g; s|__AGENT_HOME__|${AGENT_HOME}|g" \
  "${REPO}/files/systemd/claude-remote-control@.service" \
  > /etc/systemd/system/claude-remote-control@.service

# ---------------------------------------------------------------- Control plane (on-demand start/stop)
# Small Go web app at control.<preview-domain>: starts/stops playground
# remote-control servers on demand (they are NOT enabled at boot). Auth is
# Cloudflare Access at the edge + Access-JWT verification in the app. Privileged
# work goes through the scoped `nightshift-rc` wrapper via sudoers.
log "building nightshift-control"
env HOME=/root GOTOOLCHAIN=local /usr/local/go/bin/go build -C "${REPO}/control" -o /usr/local/bin/nightshift-control .
install -m0755 "${REPO}/files/control/nightshift-rc" /usr/local/bin/nightshift-rc
# sudoers: agent may run ONLY the wrapper as root (the wrapper validates its args)
printf '%s ALL=(root) NOPASSWD: /usr/local/bin/nightshift-rc\n' "${AGENT_USER}" \
  > /etc/sudoers.d/nightshift-control
chmod 440 /etc/sudoers.d/nightshift-control
visudo -cf /etc/sudoers.d/nightshift-control
# Revoke the cloud-init blanket grant: the boot-time users: block used to give
# the agent NOPASSWD:ALL + the sudo group, which made every scoped boundary in
# this repo (the wrapper above, the contract's "never touch the box" rules)
# bypassable by any unattended session. Root access for the operator is root@
# SSH; the agent needs exactly the one wrapper entry above.
rm -f /etc/sudoers.d/90-cloud-init-users
gpasswd -d "${AGENT_USER}" sudo 2>/dev/null || true
sed "s|__AGENT_USER__|${AGENT_USER}|g; s|__AGENT_HOME__|${AGENT_HOME}|g" \
  "${REPO}/files/systemd/nightshift-control.service" \
  > /etc/systemd/system/nightshift-control.service

# ---------------------------------------------------------------- Night runs (ADR-0005)
# Scheduled unattended sessions: the control plane mints/fires jobs, nightshift-rc
# starts them as transient units running the pty launcher, which composes
# contract + task and execs `claude --remote-control` (pty is load-bearing:
# without one the flag silently degrades to --print and never registers).
log "installing night-run launcher + prompts + ticket CLI"
install -m0755 "${REPO}/files/nightrun/nightshift-run-launcher" /usr/local/bin/nightshift-run-launcher
# active Claude-auth pre-flight probe: the launcher runs it to skip doomed waves,
# the control plane runs it for auth-health on the page (ADR-0006)
install -m0755 "${REPO}/files/nightrun/claude-auth-probe" /usr/local/bin/claude-auth-probe
# forge-token pre-flight probe: the launcher runs it after the Claude probe; a
# dead GH/GitLab token warns the wave (analysis-only) instead of skipping it
install -m0755 "${REPO}/files/nightrun/forge-auth-probe" /usr/local/bin/forge-auth-probe
# codex-auth pre-flight probe (ADR-0018): the launcher runs it before a
# codex-executor wave; owns its own serializing flock (codex-auth.lock)
install -m0755 "${REPO}/files/nightrun/codex-auth-probe" /usr/local/bin/codex-auth-probe
# agent-side ticketboard CLI: writes ~/.nightshift/tickets/<agent>/ directly
# (agent-owned files; no sudo, no new privilege surface)
install -m0755 "${REPO}/files/ticket/nightshift-ticket" /usr/local/bin/nightshift-ticket
# agent-side read-only view of the observability store (ADR-0006); cwd-scoped,
# fail-closed. Lets the synth wave read night facts instead of guessing.
install -m0755 "${REPO}/files/obs/nightshift-obs" /usr/local/bin/nightshift-obs
# cwd-scoped flow inspection + deadline proposals (ADR-0015)
install -m0755 "${REPO}/files/flow/nightshift-flow" /usr/local/bin/nightshift-flow
install -d -o "${AGENT_USER}" -g "${AGENT_USER}" \
  "${AGENT_HOME}/.nightshift" "${AGENT_HOME}/.nightshift/jobs" \
  "${AGENT_HOME}/.nightshift/reports" "${AGENT_HOME}/.nightshift/sweep" \
  "${AGENT_HOME}/.nightshift/tickets" "${AGENT_HOME}/.nightshift/plans" \
  "${AGENT_HOME}/.nightshift/research" "${AGENT_HOME}/.nightshift/research/ideas" \
  "${AGENT_HOME}/.nightshift/focus" "${AGENT_HOME}/.nightshift/state" \
  "${AGENT_HOME}/.nightshift/briefs" "${AGENT_HOME}/.nightshift/retro" \
  "${AGENT_HOME}/.nightshift/nodes" "${AGENT_HOME}/.nightshift/flows" \
  "${AGENT_HOME}/.nightshift/node-defs" "${AGENT_HOME}/.nightshift/proposals" \
  "${AGENT_HOME}/.nightshift/flow-templates"
install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 \
  "${REPO}/files/nightrun/contract.md" "${AGENT_HOME}/.nightshift/contract.md"
# Node prompts are operator-owned with UI-side history (ADR-0016): seed once,
# never clobber — a rerun must not silently revert control-page edits.
for f in "${REPO}"/files/nightrun/nodes/*.md; do
  dst="${AGENT_HOME}/.nightshift/nodes/$(basename "$f")"
  if [ ! -f "$dst" ]; then
    install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 "$f" "$dst"
  fi
done
for f in "${REPO}"/files/nightrun/sweep/*.md; do
  install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 "$f" "${AGENT_HOME}/.nightshift/sweep/$(basename "$f")"
done
# research profile = operator-tunable variables; seed once, never clobber edits
if [ ! -f "${AGENT_HOME}/.nightshift/research/profile.md" ]; then
  install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 \
    "${REPO}/files/nightrun/research-profile.md" "${AGENT_HOME}/.nightshift/research/profile.md"
fi
# focus files = operator north stars (Lane A bets, Lane B curated projects);
# seed once, never clobber edits
for focus in products projects; do
  if [ ! -f "${AGENT_HOME}/.nightshift/focus/${focus}.md" ]; then
    install -o "${AGENT_USER}" -g "${AGENT_USER}" -m 0644 \
      "${REPO}/files/nightrun/focus/${focus}.md" "${AGENT_HOME}/.nightshift/focus/${focus}.md"
  fi
done

# ---------------------------------------------------------------- Prompt-sync timer
# Every 15 min: ff-only pull of /opt/nightshift + re-copy ONLY prompts/contract/
# launcher+probes (never rebuilds the Go binary, never restarts services).
log "installing prompt-sync timer"
install -m0755 "${REPO}/files/control/nightshift-sync" /usr/local/bin/nightshift-sync
install -m0644 "${REPO}/files/systemd/nightshift-sync.service" /etc/systemd/system/nightshift-sync.service
install -m0644 "${REPO}/files/systemd/nightshift-sync.timer"   /etc/systemd/system/nightshift-sync.timer
# The sync service runs as root with no HOME, so git loads only /etc/gitconfig —
# without a system-level safe.directory the agent-owned repo trips "dubious
# ownership" and every sync fails. (Interactive root git works via ~/.gitconfig.)
git config --system --get-all safe.directory 2>/dev/null | grep -qx /opt/nightshift \
  || git config --system --add safe.directory /opt/nightshift

systemctl daemon-reload
systemctl enable --now nightshift-control || true
# enable --now is a no-op on an already-running unit, so a rerun would keep
# serving the OLD binary after the rebuild above (bit the 2026-07-06 ADR-0013
# deploy). try-restart only touches it if it's running; night-run transient
# units are separate and unaffected.
systemctl try-restart nightshift-control || true
systemctl enable --now nightshift-sync.timer || true
log "control plane up on :8787 (route via traefik control.${PREVIEW_DOMAIN}; set ACCESS_* in /etc/nightshift/secrets/control.env)"

log "install.sh complete. See terraform output 'next_steps' for manual steps."
