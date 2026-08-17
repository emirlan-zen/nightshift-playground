# Nightshift Playground

Nightshift Playground provisions a personal remote development box with one
workspace: `~/workspace/playground`.

It includes Terraform for a Hetzner VM, Claude Code remote-control sessions, a
Go control plane with an embedded React UI, scheduled agent runs, reports,
tickets, flows, observability, Docker, Traefik, GitHub CLI, and Codex CLI.

This is a sanitized, single-workspace distribution. It contains no credentials,
personal repository list, personal domain, host address, email, cloud account,
or organization-specific workflow. Its Git history starts at the sanitized
snapshot so removed details are not recoverable from history.

## Quick start

1. Read [SETUP.md](SETUP.md).
2. Copy `terraform/terraform.tfvars.example` to `terraform/terraform.tfvars`.
3. Replace every `REPLACE_ME` value.
4. Run `make init`, `make plan`, and `make apply`.
5. Follow `terraform output -raw next_steps`.

## Local development

```sh
make control-test
make web
make dev
```

`make dev` serves the API on `127.0.0.1:8787` and the Vite UI on
`http://localhost:5173` with no production side effects.

## Layout

```text
bootstrap/   first-boot installation and workspace setup
control/     Go control plane and React UI
files/       installed scripts, units, prompts, and service configs
terraform/   VM, network, and cloud-init configuration
workspace/   generic instructions copied into the playground workspace
```

Read [SECURITY.md](SECURITY.md) before provisioning.
