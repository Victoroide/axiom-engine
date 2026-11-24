#!/usr/bin/env bash
set -euo pipefail

tag=${1:?Pass the release tag, for example v0.1.1}
changelog=${2:-CHANGELOG.md}

release_notes() {
  awk -v heading="## [${tag#v}]" '
    found && /^## |^\[[^]]+\]: / { exit }
    index($0, heading " ") == 1 || $0 == heading { found = 1 }
    found { print }
    END { if (!found) exit 1 }
  ' "$@"
}

published=$(git show "$tag:CHANGELOG.md" | release_notes)
current=$(release_notes "$changelog")
diff -u --label "$tag" --label "$changelog" <(printf '%s\n' "$published") <(printf '%s\n' "$current")
