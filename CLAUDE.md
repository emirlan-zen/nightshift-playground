# Nightshift Playground

This repository provisions a personal remote development environment. It
supports one agent and workspace: `playground` at `~/workspace/playground`.

## Development rules

- Keep examples user-agnostic. Never commit real names, emails, IP addresses,
  domains, tokens, account IDs, repository lists, or cloud identifiers.
- Keep the default agent list restricted to `playground` in config, systemd,
  privileged wrappers, tests, and documentation.
- Runtime secrets belong in `/etc/nightshift/secrets`, never in Git.
- `NIGHTSHIFT_DEV=1` must not invoke production systemd or sudo operations.
- Rebuild `control/web/dist` after changing web source because Go embeds it.
- Run `make control-test` and the web checks before publishing changes.

See `README.md`, `SETUP.md`, and `SECURITY.md` for operator guidance.
