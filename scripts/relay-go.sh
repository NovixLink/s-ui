#!/bin/sh
# Build/test against the pinned uTLS source plus the small REALITY accessor patch.
# Cached module sources are not patched; tracked go.mod/go.sum stay unchanged.
set -eu
command_name=${1:?usage: scripts/relay-go.sh build|test|vet [go flags and packages]}
shift
case "$command_name" in build|test|vet) ;; *) echo "unsupported go command: $command_name" >&2; exit 1 ;; esac
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
mkdir -p .cache
build_dir=$(mktemp -d "$project_dir/.cache/relay-build.XXXXXX")
go mod download github.com/metacubex/utls
module_dir=$(go list -m -f '{{.Dir}}' github.com/metacubex/utls)
cp -R "$module_dir" "$build_dir/utls"
chmod -R u+w "$build_dir/utls"
GIT_CEILING_DIRECTORIES="$build_dir" git -C "$build_dir/utls" apply --unidiff-zero "$project_dir/patches/utls-reality-short-id.patch"
cp go.mod "$build_dir/go.mod"
cp go.sum "$build_dir/go.sum"
go mod edit -modfile="$build_dir/go.mod" -replace="github.com/metacubex/utls=$build_dir/utls"
exec go "$command_name" -modfile="$build_dir/go.mod" "$@"
