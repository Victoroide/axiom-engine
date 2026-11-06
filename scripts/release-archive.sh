#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
version=${1:?usage: release-archive.sh VERSION OUTPUT_DIRECTORY}
output=${2:?usage: release-archive.sh VERSION OUTPUT_DIRECTORY}
if [[ ! "$version" =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]]; then
  echo "Version must contain only letters, digits, dots, underscores and hyphens" >&2
  exit 1
fi
if [[ ! "${SOURCE_DATE_EPOCH:-}" =~ ^[0-9]+$ ]]; then
  echo "SOURCE_DATE_EPOCH must be a nonnegative integer" >&2
  exit 1
fi
os=$(go env GOHOSTOS)
arch=$(go env GOHOSTARCH)
case "$os/$arch" in
  linux/amd64) library=libbaml_cffi-x86_64-unknown-linux-gnu.so; extension=so ;;
  linux/arm64) library=libbaml_cffi-aarch64-unknown-linux-gnu.so; extension=so ;;
  darwin/arm64) library=libbaml_cffi-aarch64-apple-darwin.dylib; extension=dylib ;;
  *) echo "Unsupported release platform: $os/$arch" >&2; exit 1 ;;
esac
tar_command=tar
if command -v gtar >/dev/null; then
  tar_command=gtar
fi
if ! "$tar_command" --version | grep -q 'GNU tar'; then
  echo "GNU tar is required to create release archives" >&2
  exit 1
fi
mkdir -p "$output"
output=$(cd "$output" && pwd)
scratch=$(mktemp -d "$output/.archive.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
name="axiom-engine_${version}_${os}_${arch}"
mkdir "$scratch/$name"
cp bin/axiom-server "$scratch/$name/axiom-server"
cp "bin/libbaml_cffi.$extension" "$scratch/$name/$library"
cp LICENSE README.md "$scratch/$name/"
chmod 755 "$scratch/$name" "$scratch/$name/axiom-server" "$scratch/$name/$library"
chmod 644 "$scratch/$name/LICENSE" "$scratch/$name/README.md"
"$tar_command" --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner \
  --format=ustar -cf - -C "$scratch" "$name" | gzip -n > "$output/$name.tar.gz"
echo "$output/$name.tar.gz"
