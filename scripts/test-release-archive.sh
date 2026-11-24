#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
test_dir=$(cd "${RELEASE_TEST_DIR:-${TMPDIR:-/tmp}}" && pwd)
scratch=$(mktemp -d "$test_dir/axiom-release-test.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
export SOURCE_DATE_EPOCH=1700000000
unset RELEASE_NATIVE_INVENTORY

./scripts/release-archive.sh 1.2.3 "$scratch/first"
sleep 1
./scripts/release-archive.sh 1.2.3 "$scratch/second"
name="axiom-engine_1.2.3_$(go env GOHOSTOS)_$(go env GOHOSTARCH)"
archive="$scratch/first/$name.tar.gz"
cmp "$archive" "$scratch/second/$name.tar.gz"
extracted="$scratch/extracted archive"
mkdir "$extracted"
tar -xzf "$archive" -C "$extracted"
directory="$extracted/$name"
cmp bin/axiom-server "$directory/axiom-server"
cmp scripts/axiom-server-launch "$directory/axiom-server-launch"
test -x "$directory/axiom-server"
test -x "$directory/axiom-server-launch"
cmp LICENSE "$directory/LICENSE"
cmp README.md "$directory/README.md"
shopt -s nullglob
libraries=("$directory"/libbaml_cffi-*)
test "${#libraries[@]}" -eq 1
case "$(go env GOHOSTOS)" in
  darwin) cmp bin/libbaml_cffi.dylib "${libraries[0]}" ;;
  linux) cmp bin/libbaml_cffi.so "${libraries[0]}" ;;
esac
tar -tvzf "$archive" | awk '{print $1, $NF}' | LC_ALL=C sort > "$scratch/archive-members"
{
  printf 'drwxr-xr-x %s/\n' "$name"
  printf '%s\n' "$name/LICENSE" "$name/README.md" | sed 's/^/-rw-r--r-- /'
  printf '%s\n' "$name/axiom-server" "$name/axiom-server-launch" \
    "$name/$(basename "${libraries[0]}")" | sed 's/^/-rwxr-xr-x /'
} | LC_ALL=C sort > "$scratch/expected-members"
diff -u "$scratch/expected-members" "$scratch/archive-members"
echo "Default archive members and modes verified"
test ! -e "$directory/baml-native.cdx.json"

./scripts/release-archive.sh 1.2.3 "$scratch/diagnostic" 1
diagnostic_archive="$scratch/diagnostic/$name.tar.gz"
tar -tvzf "$diagnostic_archive" | awk '{print $1, $NF}' | LC_ALL=C sort > "$scratch/diagnostic-members"
{
  cat "$scratch/archive-members"
  printf '%s\n' "-rwxr-xr-x $name/axiom-baml-check"
} | LC_ALL=C sort > "$scratch/expected-diagnostic-members"
diff -u "$scratch/expected-diagnostic-members" "$scratch/diagnostic-members"
mkdir "$scratch/diagnostic-extracted"
tar -xzf "$diagnostic_archive" -C "$scratch/diagnostic-extracted"
diagnostic_directory="$scratch/diagnostic-extracted/$name"
for member in "$directory"/*; do
  cmp "$member" "$diagnostic_directory/$(basename "$member")"
done
cmp bin/axiom-baml-check "$diagnostic_directory/axiom-baml-check"

platform="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
baml_version=$(sed -n 's/^ARG BAML_LIB_VERSION=//p' Dockerfile)
check_diagnostic() {
  local path=$1 exit_code=$2 status=$3 reason=$4 result=0
  env -i "BAML_LIBRARY_PATH=$path" "AWS_SECRET_ACCESS_KEY=private-archive-canary" \
    "$diagnostic_directory/axiom-baml-check" > "$scratch/diagnostic.stdout" 2> "$scratch/diagnostic.stderr" || result=$?
  test "$result" -eq "$exit_code"
  test ! -s "$scratch/diagnostic.stdout"
  printf '{"status":"%s","reason":"%s","platform":"%s","baml_version":"%s"}\n' \
    "$status" "$reason" "$platform" "$baml_version" > "$scratch/diagnostic.expected"
  diff -u "$scratch/diagnostic.expected" "$scratch/diagnostic.stderr"
}
diagnostic_library="$diagnostic_directory/$(basename "${libraries[0]}")"
check_diagnostic "$diagnostic_library" 0 ok verified
cp "$diagnostic_library" "$scratch/private-archive-canary"
size=$(wc -c < "$scratch/private-archive-canary" | tr -d ' ')
last_byte=$(od -An -t u1 -j "$((size - 1))" -N 1 "$scratch/private-archive-canary" | tr -d ' ')
printf '%b' "\\$(printf '%03o' "$((last_byte ^ 1))")" |
  dd of="$scratch/private-archive-canary" bs=1 seek="$((size - 1))" conv=notrunc 2>/dev/null
check_diagnostic "$scratch/private-archive-canary" 1 error digest_mismatch
echo "Archive diagnostic verified the pinned library and rejected one changed byte"

if [[ "${1:-}" != --runtime-only ]]; then
  target="$(go env GOHOSTOS)/$(go env GOHOSTARCH)"
  schema="$PWD/bin/cyclonedx-1.7.2"
  ./scripts/native-inventory-schema.sh "$schema"
  RELEASE_NATIVE_INVENTORY=true ./scripts/release-archive.sh 1.2.3 "$scratch/inventory"
  RELEASE_NATIVE_INVENTORY=true ./scripts/release-archive.sh 1.2.3 "$scratch/inventory-repeat"
  inventory_archive="$scratch/inventory/$name.tar.gz"
  cmp "$inventory_archive" "$scratch/inventory-repeat/$name.tar.gz"
  digest=$(shasum -a 256 "$inventory_archive" | awk '{print $1}')
  verify_inventory() {
    go -C tools run ./native-inventory -target "$target" -schema-dir "$schema" -archive-sha256 "$1" verify-archive "$2"
  }
  verify_inventory "$digest" "$inventory_archive"
  echo "Verified native inventory for $target in $name.tar.gz ($digest)"

  mkdir "$scratch/modified"
  tar -xzf "$inventory_archive" -C "$scratch/modified"
  modified="$scratch/modified/$name"
  inventory="$modified/baml-native.cdx.json"
  cp "$inventory" "$scratch/original.cdx.json"
  sed -E 's/("version": "[^"]+)"/\1-wrong"/' "$scratch/original.cdx.json" > "$inventory"
  modified_archive="$scratch/modified/$name.tar.gz"
  COPYFILE_DISABLE=1 tar --format=ustar -czf "$modified_archive" -C "$scratch/modified" "$name"
  if verify_inventory "$digest" "$modified_archive" >"$scratch/inventory-error" 2>&1; then
    echo 'Changed archive matched the original archive digest' >&2
    exit 1
  fi
  grep -Fq 'archive SHA-256' "$scratch/inventory-error"
  modified_digest=$(shasum -a 256 "$modified_archive" | awk '{print $1}')
  if verify_inventory "$modified_digest" "$modified_archive" >"$scratch/inventory-error" 2>&1; then
    echo 'Accepted an incorrect native library version in the inventory' >&2
    exit 1
  fi
  grep -Fq 'inventory does not match' "$scratch/inventory-error"
  cp "$scratch/original.cdx.json" "$inventory"
  printf 'tampered' >> "$modified/$(basename "${libraries[0]}")"
  COPYFILE_DISABLE=1 tar --format=ustar -czf "$modified_archive" -C "$scratch/modified" "$name"
  modified_digest=$(shasum -a 256 "$modified_archive" | awk '{print $1}')
  if verify_inventory "$modified_digest" "$modified_archive" >"$scratch/inventory-error" 2>&1; then
    echo 'Accepted tampered native library bytes' >&2
    exit 1
  fi
  grep -Fq 'native library SHA-256' "$scratch/inventory-error"
  echo 'Archive digest, component version and native byte substitutions rejected'
fi

awk '
  /^#### Run an extracted archive$/ { section = 1 }
  section && /^```sh$/ { code = 1; next }
  code && /^```$/ { exit }
  code { print }
' "$directory/README.md" > "$scratch/archive-env.sh"
if [[ ! -s "$scratch/archive-env.sh" ]]; then
  echo "The bundled README is missing the offline archive setup" >&2
  exit 1
fi
(
  cd "$directory"
  unset BAML_LIBRARY_PATH BAML_LIBRARY_DISABLE_DOWNLOAD
  # shellcheck source=/dev/null
  source "$scratch/archive-env.sh"
  test "$BAML_LIBRARY_PATH" = "${libraries[0]}"
  test "$BAML_LIBRARY_DISABLE_DOWNLOAD" = true
)
grep -Fxq './axiom-server-launch' "$directory/README.md"
probe="$scratch/launcher environment"
mkdir "$probe"
cp "$directory/axiom-server-launch" "$probe/"
cat > "$probe/axiom-server" <<'SH'
#!/bin/sh
set -eu
test "$#" -eq 1 && test "$1" = mcp || exit 1
test "${BAML_LOG-}" = off && test "${BAML_INTERNAL_LOG-}" = off || exit 1
if env | LC_ALL=C grep -Eiq '^(BAML_TRACE_FILE|BOUNDARY_API_KEY|BOUNDARY_SECRET)='; then
  printf '%s\n' 'The launched binary inherited a native trace setting' >&2
  exit 1
fi
SH
chmod 755 "$probe/axiom-server"

# The legacy exporter accepts every capitalization of the SECRET suffix.
keys=(BOUNDARY_)
for pair in Ss Ee Cc Rr Ee Tt; do
  variants=()
  for key in "${keys[@]}"; do
    variants+=("$key${pair:0:1}" "$key${pair:1:1}")
  done
  keys=("${variants[@]}")
done
settings=("PATH=$probe:/usr/bin:/bin" BAML_LOG=warning BAML_INTERNAL_LOG=trace BAML_TRACE_FILE=fixture BOUNDARY_API_KEY=fixture)
for key in "${keys[@]}"; do
  settings+=("$key=fixture")
done
env -i "${settings[@]}" "$probe/axiom-server-launch" mcp
echo 'Launcher exec environment verified with all 64 legacy credential spellings'

for guide in "$directory/README.md" docs/mcp.md; do
  awk '
    /^```sh$/ { shell = 1; block = ""; invokes_mcp = 0; next }
    shell && /^```$/ { if (invokes_mcp) printf "%s", block; shell = 0; next }
    shell {
      block = block $0 "\n"
      if ($0 ~ /(^|[[:space:]])(\.\/)?axiom-server(-launch)?[[:space:]]+mcp[[:space:]]*$/) invokes_mcp = 1
    }
  ' "$guide" > "$scratch/mcp-examples.sh"
  test -s "$scratch/mcp-examples.sh"
  if ! (cd "$probe" && env -i "${settings[@]}" bash -e "$scratch/mcp-examples.sh"); then
    printf 'Unsafe MCP launch example in %s\n' "$(basename "$guide")" >&2
    exit 1
  fi
done
echo 'Offline archive setup and MCP examples verified'
RELEASE_ARCHIVE_TEST_INSTALL="$directory" BAML_LIBRARY_PATH="${libraries[0]}" \
  CGO_ENABLED=1 BAML_LIBRARY_DISABLE_DOWNLOAD=true BAML_LOG=off \
  go test -trimpath ./cmd/server -run '^TestReleaseArchiveLauncher$' -count=1 -v
./scripts/release-archive-smoke.sh "$archive"
for token_variable in AXIOM_INTERNAL_TOKEN AXIOM_INTERNAL_TOKEN_PREVIOUS; do
  echo "Checking archive smoke with inherited $token_variable"
  (
    unset AXIOM_INTERNAL_TOKEN AXIOM_INTERNAL_TOKEN_PREVIOUS
    export "$token_variable=archive-smoke-token-0123456789abcdef"
    ./scripts/release-archive-smoke.sh "$archive"
  )
done

echo 'Checking archive smoke with inherited verified authentication'
AXIOM_AUTH_MODE=verified AXIOM_AUTH_ISSUER=https://issuer.example \
  AXIOM_AUTH_AUDIENCE=https://api.example AXIOM_AUTH_JWKS_URL=https://issuer.example/keys \
  AXIOM_AUTH_ALGORITHMS=ES256 AXIOM_AUTH_TENANT_CLAIM=org AXIOM_AUTH_SCOPE_CLAIM=permissions \
  AXIOM_AUTH_CLIENT_ID_CLAIM=client_id AXIOM_AUTH_REGISTERED_CLIENTS=registered \
  ./scripts/release-archive-smoke.sh "$archive"

if ./scripts/release-archive.sh ../invalid "$scratch/invalid" >"$scratch/invalid.log" 2>&1; then
  echo "An unsafe version was accepted" >&2
  exit 1
fi
if SOURCE_DATE_EPOCH=invalid ./scripts/release-archive.sh 1.2.3 "$scratch/invalid" >"$scratch/epoch.log" 2>&1; then
  echo "An invalid archive timestamp was accepted" >&2
  exit 1
fi
rm "${libraries[0]}"
tar -czf "$scratch/missing-library.tar.gz" -C "$extracted" "$name"
if ./scripts/release-archive-smoke.sh "$scratch/missing-library.tar.gz" >"$scratch/missing.log" 2>&1; then
  echo "An archive without its native library was accepted" >&2
  exit 1
fi
grep -F 'Expected one bundled native library' "$scratch/missing.log"
echo "Archive contents, repeatability, validation and shutdown checks passed"
