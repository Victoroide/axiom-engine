#!/usr/bin/env bash
set -euo pipefail

image=${1:-axiom-engine:local}
prefix="axiom-smoke-check-$$"
check_dir=$(mktemp -d)
cleanup() {
  docker image rm "$prefix-root" "$prefix-kill" >/dev/null 2>&1 || true
  rm -rf "$check_dir"
}
trap cleanup EXIT

expect_rejection() {
  local variant=$1 message=$2
  if /bin/bash ./scripts/container-smoke.sh "$prefix-$variant" > "$check_dir/output" 2>&1; then
    cat "$check_dir/output" >&2
    printf 'Expected %s image to fail\n' "$variant" >&2
    exit 1
  fi
  if ! grep -F "$message" "$check_dir/output"; then
    cat "$check_dir/output" >&2
    printf 'Missing failure message for %s image\n' "$variant" >&2
    exit 1
  fi
}

printf 'FROM %s\nUSER 0:0\n' "$image" | docker build -q -t "$prefix-root" -
printf 'FROM %s\nSTOPSIGNAL SIGKILL\n' "$image" | docker build -q -t "$prefix-kill" -
expect_rejection root 'container smoke: configured user is 0:0'
expect_rejection kill 'container smoke: stop exit code is 137'
printf 'Root user and unclean stop are rejected\n'
