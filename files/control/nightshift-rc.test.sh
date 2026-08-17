#!/usr/bin/env bash
# Regression test for nightshift-rc auto-stop timers.
#
# The bug (systemd #6680): a monotonic --on-active timer re-anchors to "now" on
# every `systemctl daemon-reload`, so a `make deploy` while a session/run is live
# silently restarts the full auto-stop window from reload time. The fix arms
# ABSOLUTE --on-calendar timers instead, which keep their fire time across a
# reload.
#
# This test can't run real systemd, so it stubs `systemd-run`/`systemctl` on PATH
# and asserts the arming call now uses `--on-calendar=<absolute time ≈ now+N>`
# and NEVER `--on-active`. It writes ONLY under a mktemp dir — never ~/.nightshift.
#
# Run: bash files/control/nightshift-rc.test.sh   (needs bash + GNU date only)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rc="${here}/nightshift-rc"
[[ -f "$rc" ]] || { echo "FAIL: nightshift-rc not found at $rc" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mock_log="${tmp}/systemd-run.log"
: >"$mock_log"

# --- stub systemd-run + systemctl on PATH -----------------------------------
mkdir -p "${tmp}/bin"
cat >"${tmp}/bin/systemd-run" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"${mock_log}"
exit 0
EOF
# systemctl: everything is a no-op success. `is-active` returns non-zero (unit
# not running) so the session-cap branch behaves like a fresh slot when reached.
cat >"${tmp}/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  is-active) exit 1 ;;
  *) exit 0 ;;
esac
EOF
chmod +x "${tmp}/bin/systemd-run" "${tmp}/bin/systemctl"
export PATH="${tmp}/bin:${PATH}"

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
ok()   { echo "ok: $*"; }

# Assert the most-recent systemd-run arming call used --on-calendar with an
# absolute time within tolerance of now + <mins>, and used no --on-active.
assert_calendar_within() {
  local label="$1" mins="$2" tol=120
  local line ts want got skew
  line="$(grep -- '--on-calendar=' "$mock_log" | tail -n1 || true)"
  [[ -n "$line" ]] || { fail "${label}: no systemd-run --on-calendar call logged"; return; }
  if grep -q -- '--on-active' "$mock_log"; then
    fail "${label}: found a monotonic --on-active timer (the re-anchor bug); log:"
    sed 's/^/    /' "$mock_log" >&2
    return
  fi
  ts="$(sed -E 's/.*--on-calendar=([^ ]+ [0-9:]+).*/\1/' <<<"$line")"
  got="$(date -d "$ts" +%s 2>/dev/null || true)"
  [[ -n "$got" ]] || { fail "${label}: --on-calendar value '$ts' is not a parseable absolute time"; return; }
  want=$(( $(date +%s) + mins * 60 ))
  skew=$(( got > want ? got - want : want - got ))
  if (( skew <= tol )); then
    ok "${label}: absolute stop at '$ts' (~+${mins}m, skew ${skew}s)"
  else
    fail "${label}: stop at '$ts' is ${skew}s off the expected +${mins}m (tol ${tol}s)"
  fi
}

# --- test 1: RC session start arms a 24h ABSOLUTE auto-stop ------------------
: >"$mock_log"
bash "$rc" start playground >/dev/null
assert_calendar_within "start (24h RC autostop)" $((24 * 60))

# --- test 2: night run arms an absolute auto-stop from the <id>.stop sidecar -
# The run verb reads $HOME/.nightshift/jobs/<agent>/<id>.prompt|.stop with a
# hardcoded /home/agent path, so exercise it against a copy whose agent_home is
# redirected into the tmp dir. Everything stays under mktemp.
run_rc="${tmp}/nightshift-rc-run"
sed 's#^agent_home="/home/${agent_user}"#agent_home="'"${tmp}"'/home"#' "$rc" >"$run_rc"
grep -q "agent_home=\"${tmp}/home\"" "$run_rc" || { echo "FAIL: could not redirect agent_home for test 2" >&2; exit 1; }
jobs_dir="${tmp}/home/.nightshift/jobs/playground"
mkdir -p "$jobs_dir"
: >"${jobs_dir}/testrun1.prompt"
printf '34\n' >"${jobs_dir}/testrun1.stop"   # 34-min window, like the real bug repro
: >"$mock_log"
bash "$run_rc" run playground testrun1 >/dev/null
assert_calendar_within "run (34m sidecar autostop)" 34

if (( fails > 0 )); then
  echo "FAILED (${fails})" >&2
  exit 1
fi
echo "PASS"
