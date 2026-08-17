# The operator's public key already exists in the Hetzner project; reference it
# read-only rather than re-uploading (Hetzner dedupes keys by content).
data "hcloud_ssh_key" "operator" {
  name = var.hcloud_ssh_key_name
}

# Stable IP that survives server rebuilds, so the DNS A records stay valid.
resource "hcloud_primary_ip" "ipv4" {
  name        = "${var.server_name}-ipv4"
  type        = "ipv4"
  location    = var.location
  auto_delete = false
}

resource "hcloud_firewall" "devbox" {
  name = "${var.server_name}-fw"

  # SSH - lock to operator IPs via var.ssh_allowed_ips.
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = var.ssh_allowed_ips
  }

  # HTTP/HTTPS - inbound for Traefik to SERVE preview services (not for controlling
  # the agent; remote-control is outbound-only). Open to all so previews are reachable.
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # ICMP for ping/diagnostics.
  rule {
    direction  = "in"
    protocol   = "icmp"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_server" "devbox" {
  name         = var.server_name
  server_type  = var.server_type
  image        = var.image
  location     = var.location
  ssh_keys     = [data.hcloud_ssh_key.operator.id]
  firewall_ids = [hcloud_firewall.devbox.id]

  public_net {
    ipv4_enabled = true
    ipv4         = hcloud_primary_ip.ipv4.id
    ipv6_enabled = true
  }

  user_data = templatefile("${path.module}/templates/cloud-init.yaml.tftpl", {
    agent_user          = var.agent_user
    preview_domain      = var.preview_domain
    acme_email          = var.acme_email
    nightshift_repo_url = var.nightshift_repo_url
    go_version          = var.go_version
    node_major          = var.node_major
    skills_repo_ref     = var.skills_repo_ref
    terraform_version   = var.terraform_version
    timezone            = var.timezone
    ssh_public_key      = var.ssh_public_key
  })

  labels = {
    project = "nightshift"
    role    = "devbox"
  }
}
