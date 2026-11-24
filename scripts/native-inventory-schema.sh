#!/usr/bin/env bash
set -euo pipefail

directory=${1:?usage: native-inventory-schema.sh DIRECTORY}
mkdir -p "$directory"
scratch=$(mktemp "$directory/.schema.XXXXXX")
trap 'rm -f "$scratch"' EXIT
pins=(
  'bom-1.7.schema.json 73308edec3ab2d38bfffd993e96a042b594314143b6971a6e9ed98bbb6bd76ce'
  'cryptography-defs.schema.json 027b059a729a06d591bac79a584ef04f83fc32d91a826fdba6ad3c98a10e5b44'
  'jsf-0.82.schema.json 8bae002c25e723db7ee1f26afde680ae1a2b1a8f6b4b4b0fd65dc3becb090aae'
  'spdx.schema.json 4b345e2329f209f34e960ae2a8e7cb46a166907e6a45e94978565925dc47b359'
)
for pin in "${pins[@]}"; do
  name=${pin% *}
  digest=${pin#* }
  file="$directory/$name"
  if [[ ! -f "$file" ]]; then
    curl --fail --silent --show-error --location --retry 3 --connect-timeout 10 \
      "https://raw.githubusercontent.com/CycloneDX/specification/1.7.2/schema/$name" --output "$scratch"
    printf '%s  %s\n' "$digest" "$scratch" | shasum -a 256 -c --status -
    mv "$scratch" "$file"
  fi
  printf '%s  %s\n' "$digest" "$file" | shasum -a 256 -c --status -
done
