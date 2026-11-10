#!/bin/sh
# Smoke test of a built git-inlay: the first minutes of a new user, run
# through git itself in a throwaway repository.
#
#   scripts/smoke.sh [<directory holding git-inlay>]
#
# Without an argument it tests the git-inlay on PATH. Unlike the Go tests,
# which isolate git's configuration, this runs under the caller's own, so
# it also shows how the tool fares beside their hook manager, global
# excludes and aliases. CI runs it against the release archive.
set -eu

if [ $# -gt 0 ]; then
	PATH="$(cd "$1" && pwd):$PATH"
	export PATH
fi

fail() {
	printf 'smoke: FAILED: %s\n' "$1" >&2
	exit 1
}

# An identity for the commits, whatever the caller's configuration holds.
export GIT_AUTHOR_NAME=smoke GIT_AUTHOR_EMAIL=smoke@example.invalid
export GIT_COMMITTER_NAME=smoke GIT_COMMITTER_EMAIL=smoke@example.invalid

tmp=$(mktemp -d "${TMPDIR:-/tmp}/git-inlay-smoke.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

command -v git-inlay
git inlay version

git init -q "$tmp/host"
cd "$tmp/host"
git -c commit.gpgsign=false commit -q --allow-empty -m initial

git inlay init team
mkdir .claude
echo '{}' >.claude/settings.json
git inlay claim team .claude
git inlay team -c commit.gpgsign=false commit -q -m settings
[ -z "$(git status --porcelain)" ] || fail "the host sees the claimed files"
git inlay which .claude/settings.json | grep -q 'team$' || fail "which does not name the inlay"

git inlay hooks install --auto-attach

# The pre-commit guard must refuse a claimed path staged by force.
git add -f .claude/settings.json
if git -c commit.gpgsign=false commit -q -m leak 2>"$tmp/refusal"; then
	fail "the host committed a claimed path"
fi
grep -q 'git-inlay' "$tmp/refusal" || fail "the commit failed, but not by the guard: $(cat "$tmp/refusal")"
git rm -q --cached .claude/settings.json

# A new worktree must get the inlay's files from the post-checkout hook.
git worktree add -q "$tmp/wt" -b feature
[ -f "$tmp/wt/.claude/settings.json" ] || fail "the new worktree lacks the inlay's files"
(cd "$tmp/wt" && git inlay check)
git inlay check

echo 'smoke: ok'
