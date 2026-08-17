// ---- Secrets (never commit; pass via terraform.tfvars [gitignored] or TF_VAR_ env) ----

variable "hcloud_token" {
  description = "Hetzner Cloud API token (Project > Security > API Tokens, Read & Write)."
  type        = string
  sensitive   = true
}

// ---- DNS (set manually; Terraform does not manage DNS) ----

variable "preview_domain" {
  description = "Host the devbox lives at. Preview services are served at <name>.<preview_domain>. You create the A records: <preview_domain> and *.<preview_domain> -> the devbox IP."
  type        = string
  default     = "night.example.com"
}

variable "acme_email" {
  description = "Email for Let's Encrypt registration (TLS-ALPN-01)."
  type        = string
  default     = "you@example.com"
}

// ---- VM ----

variable "server_name" {
  type    = string
  default = "nightshift-playground"
}

variable "server_type" {
  description = "Hetzner server type. cpx42 = 8 vCPU / 16GB / 320GB. cpx31 = 4 vCPU / 8GB."
  type        = string
  default     = "cpx42"
}

variable "location" {
  description = "Hetzner location: nbg1/fsn1/hel1 (EU), ash/hil (US)."
  type        = string
  default     = "hel1"
}

variable "image" {
  type    = string
  default = "ubuntu-24.04"
}

variable "ssh_public_key" {
  description = "Operator SSH public key (contents, not path), injected via cloud-init for the agent user."
  type        = string
}

variable "hcloud_ssh_key_name" {
  description = "Name of the EXISTING SSH key in the Hetzner project to grant root access (referenced read-only)."
  type        = string
}

variable "ssh_allowed_ips" {
  description = "CIDRs allowed to reach SSH (port 22). Explicitly set this to trusted addresses."
  type        = list(string)
  validation {
    condition     = length(var.ssh_allowed_ips) > 0 && !contains(var.ssh_allowed_ips, "0.0.0.0/0") && !contains(var.ssh_allowed_ips, "::/0")
    error_message = "ssh_allowed_ips must contain trusted CIDRs and must not expose SSH globally."
  }
}

// ---- Bootstrap config (consumed by cloud-init) ----

variable "agent_user" {
  description = "Non-root Linux user the agent and remote-control run as."
  type        = string
  default     = "agent"
}

variable "nightshift_repo_url" {
  description = "Git URL of THIS repo, cloned on the box to run bootstrap/install.sh. Must be reachable without secrets (public, or add a PAT step)."
  type        = string
}

variable "go_version" {
  type    = string
  default = "1.26.4" # >= 1.25 required by modernc.org/sqlite (observability store, ADR-0006)
}

variable "node_major" {
  type    = string
  default = "22"
}

variable "skills_repo_ref" {
  description = "Commit SHA or tag of github.com/mattpocock/skills to pin."
  type        = string
  default     = "main"
}

variable "terraform_version" {
  description = "Terraform CLI version installed on the box for the playground agent."
  type        = string
  default     = "1.9.8"
}

variable "timezone" {
  type    = string
  default = "UTC"
}
