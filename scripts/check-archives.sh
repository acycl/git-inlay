#!/bin/sh
# Check that every release archive GoReleaser built carries the binary, the
# license and the manual page.
#
#   scripts/check-archives.sh <dist directory>
set -eu

dist=$1
count=0
for archive in "$dist"/git-inlay_*.tar.gz "$dist"/git-inlay_*.zip; do
	[ -e "$archive" ] || continue
	count=$((count + 1))
	binary=git-inlay
	case "$archive" in
	*.zip)
		binary=git-inlay.exe
		contents=$(unzip -Z1 "$archive")
		;;
	*)
		contents=$(tar -tzf "$archive")
		;;
	esac
	for want in "$binary" LICENSE man/git-inlay.1; do
		printf '%s\n' "$contents" | grep -qx "$want" || {
			printf 'check-archives: %s lacks %s; it holds:\n%s\n' "$archive" "$want" "$contents" >&2
			exit 1
		}
	done
	printf 'check-archives: %s ok\n' "$archive"
done
# Linux, macOS and Windows, each for amd64 and arm64.
[ "$count" -eq 6 ] || {
	printf 'check-archives: found %d archives in %s, want 6\n' "$count" "$dist" >&2
	exit 1
}
