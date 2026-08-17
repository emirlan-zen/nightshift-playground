# Setup

This guide creates one `playground` workspace on an Ubuntu VM.

## Prerequisites

- Terraform 1.5 or newer.
- A Hetzner Cloud project and read/write API token.
- An SSH public key already registered in that project.
- A domain you control, with permission to create DNS records.
- A GitHub token scoped only to repositories the playground may use.
- A Claude Code account. Codex is optional and authenticated separately.

## 1. Configure Terraform

```sh
cp terraform/terraform.tfvars.example terraform/terraform.tfvars
```

Replace every `REPLACE_ME` value, including your domain, email, SSH key name,
public key, allowed SSH CIDRs, and this repository's clone URL.

```sh
make init
make plan
make apply
terraform -chdir=terraform output dns_records_to_create
```

Create the printed DNS records.

## 2. Configure the VM

SSH as root using the IP from `terraform output`, then create a secrets file:

```sh
install -d -m 700 -o agent -g agent /etc/nightshift/secrets
install -m 600 -o agent -g agent /dev/null /etc/nightshift/secrets/playground.env
```

Add only credentials you intend the playground to use. A typical file contains:

```dotenv
GH_TOKEN=YOUR_FINE_GRAINED_GITHUB_TOKEN
```

Clone this repository to `/opt/nightshift` using your preferred authenticated
Git method, then run:

```sh
bash /opt/nightshift/bootstrap/install.sh
```

## 3. Authenticate tools

```sh
sudo -u agent -i claude auth login
sudo -u agent -i codex login        # optional
sudo -u agent -i nightshift-post-auth
```

The shipped repository manifest is empty. Add your own repositories to
`bootstrap/repos.yaml` or clone them manually below `~/workspace/playground`.
Then `nightshift-clone-repos` can clone manifest entries.

## 4. Configure control-plane authentication

Copy `files/control/control.env.example` to
`/etc/nightshift/secrets/control.env`, replace its placeholders, and run:

```sh
systemctl restart nightshift-control
```

Cloudflare Access JWTs are expected by default. Adapt the authentication layer
before exposing port 8787 through another proxy.

## 5. Verify

```sh
systemctl status nightshift-control nightshift-sync.timer
sudo nightshift-rc status playground
curl -fsS http://127.0.0.1:8787/api/health
```

Open `https://control.<your preview domain>` after DNS and access are ready.

## Updating

```sh
git -C /opt/nightshift pull --ff-only
bash /opt/nightshift/bootstrap/install.sh
```

## Removing the VM

`make destroy` deletes Terraform-managed compute resources. Review the plan and
back up workspace data first. The primary IP is retained unless removed
separately.
