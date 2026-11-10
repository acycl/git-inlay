#!/bin/sh
# Smoke test of a built git-inlay: the first minutes of a new user, run
# through git itself in a throwaway repository.
#
#   scripts/smoke.sh [<directory holding git-inlay> [<expected version>]]
#
# Without an argument it tests the git-inlay on PATH. Unlike the Go tests,
# which isolate git's configuration, this runs under the caller's own, so
# it also shows how the tool fares beside their hook manager, global
# excludes and aliases. CI runs it against the release archive, with the
# version the archive is named for.
set -eu

fail() {
	printf 'smoke: FAILED: %s\n' "$1" >&2
	exit 1
}

if [ $# -gt 0 ]; then
	dir=$(cd "$1" && pwd)
	# The binary under test must be the one in the directory given, not
	# another that happens to be installed.
	[ -x "$dir/git-inlay" ] || [ -x "$dir/git-inlay.exe" ] || fail "no git-inlay in $dir"
	PATH="$dir:$PATH"
	export PATH
	case "$(command -v git-inlay)" in
	"$dir"/*) ;;
	*) fail "git-inlay resolves to $(command -v git-inlay), not to $dir" ;;
	esac
fi

# An identity for the commits, whatever the caller's configuration holds.
export GIT_AUTHOR_NAME=smoke GIT_AUTHOR_EMAIL=smoke@example.invalid
export GIT_COMMITTER_NAME=smoke GIT_COMMITTER_EMAIL=smoke@example.invalid

tmp=$(mktemp -d "${TMPDIR:-/tmp}/git-inlay-smoke.XXXXXX")
# Leave the directory first: Windows cannot remove the one a process is in.
trap 'cd / && rm -rf "$tmp"' EXIT

command -v git-inlay
version=$(git inlay version)
printf '%s\n' "$version"
if [ $# -gt 1 ]; then
	first=$(printf '%s\n' "$version" | head -n 1)
	[ "$first" = "git-inlay $2" ] || fail "the binary reports \"$first\", not version $2"
fi

git init -q "$tmp/host"
git init -q --bare "$tmp/host.git"
cd "$tmp/host"
git -c commit.gpgsign=false commit -q --allow-empty -m initial
git remote add origin "$tmp/host.git"
git push -q origin HEAD

git inlay init team
mkdir .claude
echo '{}' >.claude/settings.json
git inlay claim team .claude
git inlay team -c commit.gpgsign=false commit -q -m settings
status=$(git status --porcelain)
[ -z "$status" ] || fail "the host sees the claimed files: $status"
git inlay which .claude/settings.json | grep -q 'team$' || fail "which does not name the inlay"

git inlay hooks install --auto-attach

# The pre-commit guard must refuse a claimed path staged by force.
git add -f .claude/settings.json
if git -c commit.gpgsign=false commit -q -m leak 2>"$tmp/refusal"; then
	fail "the host committed a claimed path"
fi
grep -q 'belongs to inlay team' "$tmp/refusal" || fail "the commit failed, but not by the guard: $(cat "$tmp/refusal")"

# The pre-push guard must refuse the same path committed past the first.
git -c commit.gpgsign=false commit -q --no-verify -m leak
if git push -q origin HEAD 2>"$tmp/refusal"; then
	fail "the host pushed a claimed path"
fi
grep -q 'belongs to inlay team' "$tmp/refusal" || fail "the push failed, but not by the guard: $(cat "$tmp/refusal")"
git reset -q --soft HEAD~1
git rm -q --cached .claude/settings.json

# A new worktree must get the inlay's files from the post-checkout hook.
git worktree add -q "$tmp/wt" -b feature
[ -f "$tmp/wt/.claude/settings.json" ] || fail "the new worktree lacks the inlay's files"
(cd "$tmp/wt" && git inlay check)
git inlay check

echo 'smoke: ok'
