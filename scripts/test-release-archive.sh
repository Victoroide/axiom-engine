#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
scratch=$(mktemp -d "${RELEASE_TEST_DIR:-${TMPDIR:-/tmp}}/axiom-release-test.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
export SOURCE_DATE_EPOCH=1700000000

./scripts/release-archive.sh 1.2.3 "$scratch/first"
sleep 1
./scripts/release-archive.sh 1.2.3 "$scratch/second"
name="axiom-engine_1.2.3_$(go env GOHOSTOS)_$(go env GOHOSTARCH)"
archive="$scratch/first/$name.tar.gz"
cmp "$archive" "$scratch/second/$name.tar.gz"
mkdir "$scratch/extracted"
tar -xzf "$archive" -C "$scratch/extracted"
directory="$scratch/extracted/$name"
cmp bin/axiom-server "$directory/axiom-server"
cmp LICENSE "$directory/LICENSE"
cmp README.md "$directory/README.md"
shopt -s nullglob
libraries=("$directory"/libbaml_cffi-*)
test "${#libraries[@]}" -eq 1
case "$(go env GOHOSTOS)" in
  darwin) cmp bin/libbaml_cffi.dylib "${libraries[0]}" ;;
  linux) cmp bin/libbaml_cffi.so "${libraries[0]}" ;;
esac
test "$(find "$directory" -type f | wc -l | tr -d ' ')" -eq 4
./scripts/release-archive-smoke.sh "$archive"
for token_variable in AXIOM_INTERNAL_TOKEN AXIOM_INTERNAL_TOKEN_PREVIOUS; do
  echo "Checking archive smoke with inherited $token_variable"
  (
    unset AXIOM_INTERNAL_TOKEN AXIOM_INTERNAL_TOKEN_PREVIOUS
    export "$token_variable=archive-smoke-token-0123456789abcdef"
    ./scripts/release-archive-smoke.sh "$archive"
  )
done

if ./scripts/release-archive.sh ../invalid "$scratch/invalid" >"$scratch/invalid.log" 2>&1; then
  echo "An unsafe version was accepted" >&2
  exit 1
fi
if SOURCE_DATE_EPOCH=invalid ./scripts/release-archive.sh 1.2.3 "$scratch/invalid" >"$scratch/epoch.log" 2>&1; then
  echo "An invalid archive timestamp was accepted" >&2
  exit 1
fi
rm "${libraries[0]}"
tar -czf "$scratch/missing-library.tar.gz" -C "$scratch/extracted" "$name"
if ./scripts/release-archive-smoke.sh "$scratch/missing-library.tar.gz" >"$scratch/missing.log" 2>&1; then
  echo "An archive without its native library was accepted" >&2
  exit 1
fi
grep -F 'Expected one bundled native library' "$scratch/missing.log"
echo "Archive contents, repeatability, validation and shutdown checks passed"
