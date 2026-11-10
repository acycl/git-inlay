#!/bin/sh
# Smoke-test the binary in the linux/amd64 release archive, and hold it to
# the version the archive is named for, or to the one given.
#
#   scripts/smoke-archive.sh <dist directory> [<expected version>]
set -eu

dist=$1
set -- "$dist"/git-inlay_*_linux_amd64.tar.gz "${2:-}"
[ -e "$1" ] || {
	printf 'smoke-archive: no linux/amd64 archive in %s\n' "$dist" >&2
	exit 1
}
archive=$1
version=$2
if [ -z "$version" ]; then
	version=${archive##*/git-inlay_}
	version=${version%_linux_amd64.tar.gz}
fi

release=$(mktemp -d "${TMPDIR:-/tmp}/git-inlay-release.XXXXXX")
trap 'rm -rf "$release"' EXIT
tar -xzf "$archive" -C "$release"
"$(dirname "$0")/smoke.sh" "$release" "$version"
