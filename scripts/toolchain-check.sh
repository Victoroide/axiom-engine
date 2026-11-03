#!/usr/bin/env bash
set -euo pipefail

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."

read_version() {
  if [[ -f "$1" ]]; then
    sed -nE "$2" "$1"
  fi
}

sites=(
  'go.mod'
  'tools/go.mod'
  'Dockerfile'
  'README.md (badge)'
  'README.md (prerequisites)'
  'CONTRIBUTING.md'
  'docs/development.md'
)
versions=(
  "$(read_version go.mod 's/^go[[:space:]]+([^[:space:]]+)[[:space:]]*$/\1/p')"
  "$(read_version tools/go.mod 's/^go[[:space:]]+([^[:space:]]+)[[:space:]]*$/\1/p')"
  "$(read_version Dockerfile 's/^FROM[[:space:]]+golang:([^-[:space:]]+)-.*/\1/p')"
  "$(read_version README.md 's|.*badge/go-([^-]+)-blue[.]svg.*|\1|p')"
  "$(read_version README.md 's/^Prerequisites: Go ([^,]+),.*/\1/p')"
  "$(read_version CONTRIBUTING.md 's/^- Go ([^,]+), matching.*/\1/p')"
  "$(read_version docs/development.md 's/^- [*][*]Go[*][*]: ([^,]+),.*/\1/p')"
)

result=0
printf '%-30s %s\n' 'Site' 'Go version'
for i in "${!sites[@]}"; do
  printf '%-30s %s\n' "${sites[$i]}" "${versions[$i]:-<missing>}"
  if [[ -z "${versions[$i]}" || "${versions[$i]}" != "${versions[0]}" ]]; then
    result=1
  fi
done
if (( result != 0 )); then
  printf 'Go version sites must all match go.mod (%s).\n' "${versions[0]:-<missing>}" >&2
fi
exit "$result"
