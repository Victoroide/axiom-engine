#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
scratch=$(mktemp -d "${RELEASE_TEST_DIR:-${TMPDIR:-/tmp}}/axiom-provenance-test.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
cat > "$scratch/bundle.json" <<'JSON'
{
  "mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json",
  "verificationMaterial": {"certificate": {"rawBytes": "Y2VydGlmaWNhdGU="}},
  "dsseEnvelope": {
    "payloadType": "application/vnd.in-toto+json",
    "payload": "eyJmb28iOiJiYXIifQ==",
    "signatures": [{"keyid": "", "sig": "c2lnbmF0dXJl"}]
  }
}
JSON
printf '%s\n' '{"payloadType":"application/vnd.in-toto+json","payload":"eyJmb28iOiJiYXIifQ==","signatures":[{"keyid":"","sig":"c2lnbmF0dXJl"}]}' > "$scratch/expected.jsonl"
./scripts/release-provenance.sh "$scratch/bundle.json" "$scratch/provenance.intoto.jsonl"
cmp "$scratch/expected.jsonl" "$scratch/provenance.intoto.jsonl"
test "$(wc -l < "$scratch/provenance.intoto.jsonl" | tr -d ' ')" -eq 1
for invalid in '{}' '{"dsseEnvelope":null}' '{"dsseEnvelope":{}}' '{"dsseEnvelope":[]}' '{invalid'; do
  printf '%s\n' "$invalid" > "$scratch/invalid.json"
  if ./scripts/release-provenance.sh "$scratch/invalid.json" "$scratch/invalid.jsonl" > "$scratch/error.log" 2>&1; then
    echo "An invalid bundle was accepted" >&2
    exit 1
  fi
done
cat "$scratch/bundle.json" "$scratch/bundle.json" > "$scratch/multiple.json"
if ./scripts/release-provenance.sh "$scratch/multiple.json" "$scratch/multiple.jsonl" > "$scratch/error.log" 2>&1; then
  echo "Multiple bundles were accepted as one statement" >&2
  exit 1
fi
echo "DSSE envelope preserved as one JSON line; invalid and multiple bundles rejected"
