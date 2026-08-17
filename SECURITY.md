# Security model

Nightshift runs coding agents with broad access to a dedicated development VM.
Treat the VM as a privileged automation environment, not a general-purpose host.

## Secrets

- Never commit credentials or real values to this repository.
- Store playground credentials in
  `/etc/nightshift/secrets/playground.env` with mode `0600`.
- Store control-plane settings in `/etc/nightshift/secrets/control.env`.
- Scope tokens to the smallest repository and permission set that works.
- Use a dedicated cloud project and dedicated credentials where practical.
- Rotate a credential immediately if it appears in logs, shell history, Git, or
  an agent transcript.

## Boundaries

- The privileged `nightshift-rc` helper accepts only `playground` and validates
  session and run identifiers.
- The agent user receives one passwordless sudo command:
  `/usr/local/bin/nightshift-rc`.
- Remote-control sessions and unattended runs have automatic stop timers.
- The control plane fails closed without Cloudflare Access configuration.
- Restrict SSH to trusted `/32` or `/128` CIDRs.
- Docker group membership is root-equivalent. It is required for preview
  hosting, so the VM does not isolate hostile code from the host.

## GitHub

Use a dedicated fine-grained token. Limit it to repositories that may be edited,
and prefer branch protection and pull-request review over direct pushes.

## Publishing modifications

Scan tracked files and Git history for emails, tokens, hostnames, IPs, usernames,
repository URLs, and cloud identifiers. Start fresh history when sanitizing an
existing private setup.

## Incident response

1. Stop the affected session or VM.
2. Revoke exposed credentials.
3. Remove leaked values from the working tree and Git history.
4. Rebuild the VM if host integrity is uncertain.
5. Review agent transcripts and service logs for further exposure.
