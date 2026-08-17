output "devbox_ipv4" {
  value = hcloud_primary_ip.ipv4.ip_address
}

output "ssh" {
  value = "ssh root@${hcloud_primary_ip.ipv4.ip_address}"
}

output "dns_records_to_create" {
  value = <<-EOT
    Create these A records (Terraform does not manage DNS):
      ${var.preview_domain}        A  ${hcloud_primary_ip.ipv4.ip_address}
      *.${var.preview_domain}      A  ${hcloud_primary_ip.ipv4.ip_address}
    Services can then use https://<name>.${var.preview_domain}
  EOT
}

output "next_steps" {
  value = <<-EOT
    1. Create the DNS records shown by dns_records_to_create.
    2. SSH to root@${hcloud_primary_ip.ipv4.ip_address} after cloud-init completes.
    3. Create /etc/nightshift/secrets/playground.env with mode 0600.
    4. Clone ${var.nightshift_repo_url} to /opt/nightshift.
    5. Run bash /opt/nightshift/bootstrap/install.sh.
    6. Authenticate Claude and optionally Codex as ${var.agent_user}.
    7. Run sudo -u ${var.agent_user} -i nightshift-post-auth.

    See SETUP.md for exact commands and control-plane authentication.
  EOT
}
