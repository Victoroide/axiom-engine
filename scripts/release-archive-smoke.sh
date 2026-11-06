#!/usr/bin/env bash
set -euo pipefail

archive=${1:?usage: release-archive-smoke.sh ARCHIVE}
archive=$(cd "$(dirname "$archive")" && pwd)/$(basename "$archive")
scratch=$(mktemp -d "$(dirname "$archive")/.smoke.XXXXXX")
pid=
cleanup() {
  if [[ -n "$pid" ]]; then
    kill -KILL "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
  rm -rf "$scratch"
}
trap cleanup EXIT
tar -xzf "$archive" -C "$scratch"
directories=("$scratch"/*)
test "${#directories[@]}" -eq 1
test -d "${directories[0]}"
directory=${directories[0]}
shopt -s nullglob
libraries=("$directory"/libbaml_cffi-*.so "$directory"/libbaml_cffi-*.dylib)
if [[ "${#libraries[@]}" -ne 1 ]]; then
  echo "Expected one bundled native library" >&2
  exit 1
fi
unset AXIOM_INTERNAL_TOKEN AXIOM_INTERNAL_TOKEN_PREVIOUS
export AXIOM_AUTH_DISABLED=true
export BAML_LIBRARY_PATH=${libraries[0]}
export BAML_LIBRARY_DISABLE_DOWNLOAD=true
export BAML_LOG=off
export PORT=${RELEASE_SMOKE_PORT:-8080}
(
  cd "$directory"
  exec ./axiom-server
) >"$scratch/server.log" 2>&1 &
pid=$!
deadline=$((SECONDS + 30))
while ! "$directory/axiom-server" healthcheck >"$scratch/healthcheck.log" 2>&1; do
  if ! kill -0 "$pid" 2>/dev/null || (( SECONDS >= deadline )); then
    cat "$scratch/server.log" "$scratch/healthcheck.log" >&2
    echo "Archive failed to become live within 30 seconds" >&2
    exit 1
  fi
  sleep 0.2
done
kill -TERM "$pid"
deadline=$((SECONDS + 15))
while kill -0 "$pid" 2>/dev/null; do
  if (( SECONDS >= deadline )); then
    cat "$scratch/server.log" >&2
    echo "Archive failed to stop after SIGTERM" >&2
    exit 1
  fi
  sleep 0.2
done
status=0
wait "$pid" || status=$?
pid=
if [[ "$status" -ne 0 ]]; then
  cat "$scratch/server.log" >&2
  echo "Archive exited with status $status after SIGTERM" >&2
  exit 1
fi
echo "Archive liveness and SIGTERM exit status 0 verified: $(basename "$archive")"
