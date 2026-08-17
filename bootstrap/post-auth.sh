#!/usr/bin/env bash
# Run as the AGENT user AFTER `claude auth login`:
#   sudo -u agent -i nightshift-post-auth
#
# Pre-accepts the playground workspace trust and remote-control consent.
set -euo pipefail

CONF="$HOME/.claude.json"
MANIFEST=/opt/nightshift/bootstrap/repos.yaml
[ -f "$CONF" ] || { echo "ERROR: $CONF missing — run 'claude auth login' first."; exit 1; }

companies=playground

python3 - "$CONF" $companies <<'PY'
import json, sys, os
conf, companies = sys.argv[1], sys.argv[2:]
d = json.load(open(conf)); proj = d.setdefault("projects", {})
for c in companies:
    ws = os.path.expanduser(f"~/workspace/{c}")
    e = proj.setdefault(ws, {})
    e["hasTrustDialogAccepted"] = True
    e["hasCompletedProjectOnboarding"] = True
d["remoteEnabled"] = True; d["remoteDialogSeen"] = True
# night runs (ADR-0005) start `claude` interactively under a pty — they hit
# the first-run dialogs a remote-control SERVER never shows. Pre-accept:
d["theme"] = "dark"; d["hasCompletedOnboarding"] = True
json.dump(d, open(conf, "w"), indent=2)
print("trusted:", " ".join(companies))
PY

# bypassPermissions consent dialog (would block a night run's pty session);
# the accepted-flag lives in settings.json since the bypassPermissionsModeAccepted
# migration, as skipDangerousModePermissionPrompt.
python3 - "$HOME/.claude/settings.json" <<'PY'
import json, sys
conf = sys.argv[1]
d = json.load(open(conf))
d["skipDangerousModePermissionPrompt"] = True
json.dump(d, open(conf, "w"), indent=2)
print("bypassPermissions consent pre-accepted")
PY

# Remote-control servers are ON-DEMAND, not always-on: do NOT enable at boot.
# The control plane starts and stops playground sessions,
# and each start arms a 24h auto-stop. We only make sure they're not enabled.
for c in $companies; do
  sudo systemctl disable "claude-remote-control@${c}" 2>/dev/null || true
done
echo "trust pre-accepted. remote-control is on-demand:"
echo "  start via your control URL, or: sudo systemctl start claude-remote-control@playground"
