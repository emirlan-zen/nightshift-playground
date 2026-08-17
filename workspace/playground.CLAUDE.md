# Playground

This is the operator's personal development workspace. Only repositories and
services explicitly placed here are in scope.

## Safety

- Treat each repository's own instructions and branch protections as binding.
- Do not infer permission to merge, release, deploy, or mutate cloud resources.
- Never access `/etc/nightshift/secrets` or agent authentication stores.
- Do not modify the Nightshift host, proxy, control plane, or provisioning state
  from ordinary project work.
- Use only the accounts and repositories the operator configured for this
  workspace.

## Tools

- `git` and `gh` use the playground's configured GitHub token.
- `codex` is optional and works only inside this workspace.
- Docker, Node.js, pnpm, Terraform, and common build tools are installed.
- `nightshift-ticket`, `nightshift-flow`, and `nightshift-obs` expose local
  Nightshift workflows without granting additional host privileges.

Default to branch + pull request. Deployment or merging requires explicit task
authorization or a project autonomy setting in `~/.nightshift/focus/projects.md`.
