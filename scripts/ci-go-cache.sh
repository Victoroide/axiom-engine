#!/usr/bin/env bash
# Derive the GitHub Actions cache keys for one Go CI job.
#
# usage: ci-go-cache.sh modules|test|lint
#
#   modules  the public Go module cache, which every job shares
#   test     adds the build cache for race and coverage runs
#   lint     adds the build cache for vet and golangci-lint, and the
#            golangci-lint analysis cache, whose directory is GOLANGCI_LINT_CACHE
#
# Run it from the repository root after Go is set up. The step outputs go to
# standard output in the GITHUB_OUTPUT format and only after every input was
# read, so a failed run leaves the job without keys. A summary goes to standard
# error.
set -euo pipefail
export LC_ALL=C

# Raise this number to discard every entry saved under an earlier one.
generation=1

fail() {
  printf 'ci-go-cache: %s\n' "$*" >&2
  exit 1
}

# A short digest of standard input.
digest() {
  local sum
  sum=$(shasum -a 256)
  printf '%s' "${sum:0:16}"
}

# A key segment limited to characters that need no escaping. Commas separate
# restore keys, so every other character is replaced.
segment() {
  local value=${1//[^A-Za-z0-9._-]/_}
  [[ -n $value ]] || fail "$2 is empty"
  printf '%s' "$value"
}

variant=${1:-}
case $variant in
  modules | test | lint) ;;
  *)
    echo 'usage: ci-go-cache.sh modules|test|lint' >&2
    exit 2
    ;;
esac

for file in go.mod go.sum tools/go.mod tools/go.sum Makefile; do
  [[ -f $file ]] || fail "$file is missing; run from the repository root"
done

go_env=$(go env GOVERSION GOMODCACHE GOCACHE CC) || fail 'go env failed'
go_version=
module_cache=
build_cache=
compiler_command=
{
  read -r go_version
  read -r module_cache
  read -r build_cache
  read -r compiler_command
} <<<"$go_env" || true
[[ -n $go_version ]] || fail 'go env GOVERSION is empty'
[[ -n $module_cache ]] || fail 'go env GOMODCACHE is empty'

# The runner operating system, architecture and image, and the resolved Go
# version.
image=${ImageOS:-}
if [[ -z $image ]]; then
  echo '::warning::ImageOS is not set, so the cache keys do not name the runner image.' >&2
  image=unknown
fi
scope=$(segment "${RUNNER_OS:-$(uname -s)}" RUNNER_OS)
scope+=-$(segment "${RUNNER_ARCH:-$(uname -m)}" RUNNER_ARCH)
scope+=-$(segment "$image" ImageOS)
scope+=-$(segment "$go_version" GOVERSION)

# The root and tools manifests. golangci-lint runs through `go run`, so its
# version decides which modules the lint job downloads.
manifests=$(shasum -a 256 go.mod go.sum tools/go.mod tools/go.sum | digest)
lint_version=
while IFS= read -r line || [[ -n $line ]]; do
  if [[ $line == 'GOLANGCI_LINT_VERSION := '* ]]; then
    lint_version=${line#'GOLANGCI_LINT_VERSION := '}
  fi
done <Makefile
[[ -n $lint_version ]] || fail 'Makefile does not set GOLANGCI_LINT_VERSION'
lint_version=$(segment "$lint_version" GOLANGCI_LINT_VERSION)

modules_key=go-modules-v$generation-$scope-$manifests-$lint_version
outputs=$(printf 'modules-path=%s\nmodules-key=%s' "$module_cache" "$modules_key")
summary="modules $modules_key"

if [[ $variant != modules ]]; then
  [[ -n $build_cache ]] || fail 'go env GOCACHE is empty'

  # The native library pins and the C compiler decide how CGO packages build.
  [[ -f Dockerfile ]] || fail 'Dockerfile is missing; run from the repository root'
  pins=
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line == 'ARG BAML_LIB_'*=* ]]; then
      pins+=$line$'\n'
    fi
  done <Dockerfile
  [[ -n $pins ]] || fail 'Dockerfile sets no BAML_LIB pins'
  native=$(printf '%s' "$pins" | digest)

  compiler_id=ccnone
  compiler=()
  read -r -a compiler <<<"$compiler_command"
  if ((${#compiler[@]} > 0)) && command -v "${compiler[0]}" >/dev/null 2>&1; then
    compiler_line=
    { read -r compiler_line || true; } < <("${compiler[@]}" --version 2>/dev/null)
    if [[ -n $compiler_line ]]; then
      compiler_id=cc$(printf '%s' "$compiler_line" | digest)
    fi
  fi

  case $variant in
    test)
      job='test'
      paths=$build_cache
      ;;
    lint)
      [[ -n ${GOLANGCI_LINT_CACHE:-} ]] || fail 'GOLANGCI_LINT_CACHE must name the analysis cache directory'
      [[ -f .golangci.yml ]] || fail '.golangci.yml is missing; run from the repository root'
      job=lint-$lint_version-$(digest <.golangci.yml)
      paths=$build_cache$'\n'$GOLANGCI_LINT_CACHE
      ;;
  esac

  # The week bounds how old a build cache can get: a new week misses the exact
  # key, the first push to main saves a fresh entry, and Go drops entries it has
  # not used for five days.
  week=$(segment "$(date -u +%G-W%V)" week)
  prefix=go-build-v$generation-$job-$scope-$compiler_id-native$native
  build_key=$prefix-$manifests-$week
  outputs+=$'\n'"build-key=$build_key"
  outputs+=$'\n'"build-paths<<ci_go_cache_eof"$'\n'"$paths"$'\n'"ci_go_cache_eof"
  outputs+=$'\n'"build-restore-keys<<ci_go_cache_eof"$'\n'"$prefix-$manifests-"$'\n'"$prefix-"$'\n'"ci_go_cache_eof"
  summary+=$'\n'"build   $build_key"
fi

printf '%s\n' "$outputs"
printf '%s\n' "$summary" | sed 's/^/ci-go-cache: /' >&2
