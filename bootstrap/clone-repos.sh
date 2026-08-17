#!/usr/bin/env bash
# Clone configured repositories into ~/workspace/playground/.
# Run as the agent AFTER secrets are in place:
#   sudo -u agent -i nightshift-clone-repos
# Auth: GitHub via GH_TOKEN and the configured credential helper.
set -euo pipefail

MANIFEST="/opt/nightshift/bootstrap/repos.yaml"
WS="${HOME}/workspace"

if ! command -v yq >/dev/null; then
  ARCH=$(dpkg --print-architecture)
  sudo wget -qO /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/latest/download/yq_linux_${ARCH}"
  sudo chmod +x /usr/local/bin/yq
fi

clone_one() {  # $1=dest dir  $2=git url
  if [ -d "$1/.git" ]; then echo "[skip] $1"; return; fi
  echo "[clone] $2"
  git clone -q "$2" "$1"
}

for company in $(yq '.companies | keys | .[]' "${MANIFEST}"); do
  base=$(yq ".companies.${company}.base" "${MANIFEST}")
  layout=$(yq ".companies.${company}.layout" "${MANIFEST}")
  mkdir -p "${WS}/${company}"          # ensure the agent dir exists even with no repos (e.g. playground)
  if [ "${layout}" = "product" ]; then
    for product in $(yq ".companies.${company}.products | keys | .[]" "${MANIFEST}"); do
      for repo in $(yq ".companies.${company}.products.${product}[]" "${MANIFEST}"); do
        dest="${WS}/${company}/${product}/${repo}"
        mkdir -p "$(dirname "${dest}")"
        clone_one "${dest}" "${base}/${repo}.git"
      done
    done
  else  # flat
    for repo in $(yq ".companies.${company}.repos[]" "${MANIFEST}"); do
      dest="${WS}/${company}/${repo}"
      mkdir -p "${WS}/${company}"
      clone_one "${dest}" "${base}/${repo}.git"
    done
  fi
done

echo "[nightshift] clone complete."
