#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

workflow_step() {
  awk -v step="$1" '
    $0 == "      - name: "step { found = 1; next }
    found && /^        run: \|/ { body = 1; next }
    body && /^      - / { exit }
    body && /^  [[:alnum:]-]+:/ { exit }
    body { sub(/^          /, ""); print }
  ' .github/workflows/release.yml
}

mkdir "$scratch/bin" "$scratch/digests"
export PATH="$scratch/bin:$PATH"
export RUNNER_TEMP="$scratch" GITHUB_OUTPUT="$scratch/output" GITHUB_RUN_ID=12 GITHUB_RUN_ATTEMPT=1
export REGISTRY_IMAGE=example.invalid/runtime
export RELEASE_CALLS="$scratch/calls" MERGED_INDEX="$scratch/merged.json"
export METADATA_JSON='{"tags":["example.invalid/runtime:v1.2.3","example.invalid/runtime:latest"]}'
export NATIVE_INVENTORY=true
export STAGED_DIGEST=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
amd=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
arm=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
printf 'linux/amd64 %s\n' "$amd" > "$scratch/digests/${amd#sha256:}"
printf 'linux/arm64 %s\n' "$arm" > "$scratch/digests/${arm#sha256:}"
# shellcheck disable=SC2016
printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail' 'echo "$*" >> "$RELEASE_CALLS"' \
  'if [[ "$*" == "buildx imagetools inspect --raw "* ]]; then cat "$MERGED_INDEX"; fi' \
  'if [[ "$*" == "buildx imagetools inspect "*" --format "* ]]; then printf "%s\n" "$STAGED_DIGEST"; fi' \
  'if [[ "$1" == create ]]; then echo container-id; fi' \
  'if [[ "$1" == cp ]]; then' \
  '  if [[ "$2" == *baml-native.cdx.json ]]; then exit 1; fi' \
  '  printf native > "$3"' \
  'fi' 'exit 0' > "$scratch/bin/docker"
# shellcheck disable=SC2016
printf '%s\n' '#!/usr/bin/env bash' 'echo "$*" >> "$RELEASE_CALLS"' > "$scratch/bin/make"
chmod +x "$scratch/bin/docker" "$scratch/bin/make"

if [[ "${1:-merge}" == merge ]]; then
  workflow_step 'Merge platform digests' > "$scratch/merge.sh"
  test -s "$scratch/merge.sh"
  jq -n --arg amd "$amd" '{manifests:[{platform:{os:"linux",architecture:"amd64"},digest:$amd}]}' > "$MERGED_INDEX"
  STAGED_DIGEST=sha256:$(shasum -a 256 "$MERGED_INDEX" | awk '{print $1}')
  if bash -euo pipefail "$scratch/merge.sh"; then
    echo 'The merge accepted a missing runtime platform' >&2
    exit 1
  fi
  if grep -Eq -- '--tag [^ ]*:(v1\.2\.3|latest)' "$RELEASE_CALLS"; then
    echo 'Release tags moved before the index was verified' >&2
    exit 1
  fi

  : > "$RELEASE_CALLS"
  jq -n --arg amd "$amd" --arg arm "$arm" '{manifests:[{platform:{os:"linux",architecture:"amd64"},digest:$amd},{platform:{os:"linux",architecture:"arm64"},digest:$arm}]}' > "$MERGED_INDEX"
  STAGED_DIGEST=sha256:$(shasum -a 256 "$MERGED_INDEX" | awk '{print $1}')
  bash -euo pipefail "$scratch/merge.sh"
  grep -Fxq "digest=$STAGED_DIGEST" "$GITHUB_OUTPUT"
  grep -E -- "--tag [^ ]*:v1\.2\.3.* $REGISTRY_IMAGE@$STAGED_DIGEST$" "$RELEASE_CALLS" >/dev/null
  echo 'Release tags use the verified index digest'
fi

if [[ "${1:-gate}" == gate ]]; then
  publishing_jobs=(merge attest sign release publish-archives provenance)
  # Check whether a publishing job overrides the default success() condition.
  awk -v publishing_jobs="${publishing_jobs[*]}" '
    BEGIN {
      count = split(publishing_jobs, names)
      for (i = 1; i <= count; i++) publishing[names[i]] = 1
    }
    /^jobs:/ { in_jobs = 1; next }
    !in_jobs || /^[[:space:]]*#/ { next }
    /^[^[:space:]]/ { exit }
    /^  [[:alnum:]_-]+:/ {
      job = $1
      sub(/:$/, "", job)
      reading_if = 0
      next
    }
    !(job in publishing) { next }
    /^    [^[:space:]]/ { reading_if = /^    if:/ }
    reading_if {
      line = $0
      sub(/[[:space:]]+#.*$/, "", line)
      conditions[job] = conditions[job] " " line
    }
    END {
      for (job in conditions) {
        if (tolower(conditions[job]) ~ /(^|[^[:alnum:]_.-])(always|failure|cancelled)[[:space:]]*\([[:space:]]*\)/) {
          print "The " job " job condition can run after a failed dependency"
          failed = 1
        }
      }
      exit failed
    }
  ' .github/workflows/release.yml >&2
  awk '
    /^jobs:/ { jobs = 1; next }
    jobs && /^  [[:alnum:]_-]+:$/ { job = $1; sub(/:$/, "", job); next }
    jobs && job != "" && /^    needs:/ {
      sub(/^    needs:[[:space:]]*/, ""); gsub(/[][,]/, " "); print job, $0
    }
  ' .github/workflows/release.yml > "$scratch/needs"
  test -s "$scratch/needs"
  # Archive verification failed while both image builds succeeded.
  skipped=" checksums "
  while :; do
    before=$skipped
    while read -r job dependencies; do
      for dependency in $dependencies; do
        if [[ "$skipped" == *" $dependency "* && "$skipped" != *" $job "* ]]; then
          skipped="$skipped$job "
        fi
      done
    done < "$scratch/needs"
    if [[ "$skipped" == "$before" ]]; then
      break
    fi
  done
  if [[ "$skipped" == *" build "* ]]; then
    echo 'The failed archive check also skipped the image builds' >&2
    exit 1
  fi
  for job in "${publishing_jobs[@]}"; do
    if [[ "$skipped" != *" $job "* ]]; then
      echo "The $job job runs after archive verification fails" >&2
      exit 1
    fi
  done
  echo 'Image promotion and signing wait for archive verification'
fi

if [[ "${1:-archive}" == archive ]]; then
  : > "$RELEASE_CALLS"
  workflow_step 'Package and exercise native archive' > "$scratch/archive.sh"
  test -s "$scratch/archive.sh"
  RELEASE_VERSION=1.2.3 SOURCE_DATE_EPOCH=1700000000 RELEASE_NATIVE_INVENTORY=false bash -euo pipefail "$scratch/archive.sh"
  grep -q '^release-archive-runtime-test ' "$RELEASE_CALLS"
  : > "$RELEASE_CALLS"
  RELEASE_VERSION=1.2.3 SOURCE_DATE_EPOCH=1700000000 RELEASE_NATIVE_INVENTORY=true bash -euo pipefail "$scratch/archive.sh"
  grep -q '^release-archive-test ' "$RELEASE_CALLS"
  echo 'The disabled archive workflow selects the schema-independent tests'
fi

if [[ "${1:-consumer}" == consumer ]]; then
  : > "$RELEASE_CALLS"
  awk '/^To inspect an image inventory/{found=1; next} found && /^```sh$/{body=1; next} body && /^```$/{exit} body {print}' \
    docs/deployment.md > "$scratch/consumer.sh"
  test -s "$scratch/consumer.sh"
  mkdir "$scratch/consumer"
  native_hash=$(printf native | shasum -a 256 | awk '{print $1}')
  jq -n --arg hash "$native_hash" '{components:[{properties:[{name:"axiom-engine:target",value:"linux/arm64"}],hashes:[{alg:"SHA-256",content:$hash}]}]}' \
    > "$scratch/consumer/baml-native.cdx.json"
  if (cd "$scratch/consumer" && TMPDIR="$scratch" digest="$STAGED_DIGEST" bash "$scratch/consumer.sh"); then
    echo 'The consumer accepted a stale inventory after a failed copy' >&2
    exit 1
  fi
  test ! -e "$scratch/consumer/libbaml_cffi.so"
  grep -Fxq 'rm container-id' "$RELEASE_CALLS"
  echo 'The consumer stops on a failed copy and removes its container'
fi
