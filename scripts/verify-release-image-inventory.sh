#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
image=${1:?usage: verify-release-image-inventory.sh REPOSITORY TARGET DIGEST SCHEMA_DIRECTORY [OCI_ARCHIVE]}
target=${2:?missing image target}
digest=${3:?missing image digest}
schema=${4:?missing schema directory}
schema=$(cd "$schema" && pwd)
oci=${5:-}
args=(-target "$target" -schema-dir "$schema" -oci-digest "$digest")
if [[ -n "$oci" ]]; then
  oci=$(cd "$(dirname "$oci")" && pwd)/$(basename "$oci")
  manifest=$(go -C tools run ./native-inventory "${args[@]}" verify-oci "$oci")
else
  manifest=$(go -C tools run ./native-inventory "${args[@]}" verify-registry "$image")
fi
echo "Verified native inventory for $target in $digest ($manifest)" >&2
printf '%s\n' "$manifest"
