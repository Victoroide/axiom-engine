#!/usr/bin/env bash
set -euo pipefail

bundle=${1:?usage: release-provenance.sh BUNDLE OUTPUT_FILE}
output=${2:?usage: release-provenance.sh BUNDLE OUTPUT_FILE}
jq -sce '
  if length == 1 then .[0].dsseEnvelope
  else error("Expected one Sigstore bundle") end
  | if type == "object"
      and .payloadType == "application/vnd.in-toto+json"
      and (.payload | type == "string" and length > 0)
      and (.signatures | type == "array" and length > 0)
    then .
    else error("Bundle must contain a signed DSSE envelope") end
' "$bundle" > "$output"
