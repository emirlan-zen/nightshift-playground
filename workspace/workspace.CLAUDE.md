# Workspace

This host has one managed workspace: `~/workspace/playground`.

Repositories may have their own instructions; read them before making changes.
Use `nightshift-ticket` for durable follow-up work and the control page for
sessions, runs, reports, flows, and system health.

Preview services may join the external Docker network `web` and use Traefik
labels. The preview suffix comes from the operator's configured
`PREVIEW_DOMAIN`; do not hard-code a domain in project files.

Do not alter the host's Nightshift installation, proxy, systemd units, Terraform
state, or cloud resources from a project task unless the operator explicitly
scopes that infrastructure work.
