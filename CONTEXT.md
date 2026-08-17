# Project context

Nightshift Playground is a single-user, single-workspace remote development
environment. The only managed agent identifier is `playground`, mapped to
`~/workspace/playground`.

The control plane starts and stops Claude Code remote-control sessions, queues
unattended runs, records reports and observations, and exposes those controls in
a web UI. Terraform and bootstrap scripts build the host; runtime state lives
under `~/.nightshift` on that host.

Distribution rules:

- examples remain placeholders;
- no personal repositories are cloned by default;
- credentials are supplied out of band;
- prompts describe generic projects and user-selected autonomy;
- removed private setup details are not retained in Git history.
