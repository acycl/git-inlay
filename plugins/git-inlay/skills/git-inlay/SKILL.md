---
name: git-inlay
description: Work in a repository that uses git-inlay, a tool that keeps private sets of files (team Claude Code settings, personal notes, local tooling config) tracked in their own git repositories while they sit in another repository's working tree. Use when a session-start briefing mentions git-inlay or inlays; when the user mentions git inlay, an inlay, claims or a .git-inlay/ directory; when files exist on disk but never show in git status or git diff; when git-inlay refuses a commit or push; or when the user wants files kept in a repository's tree, versioned, but never committed to it.
---

# git-inlay

An **inlay** is a second git repository whose working tree is the host's:
the repository the user cloned. Each inlay owns a set of paths, its
**claims**, recorded in `.git-inlay/<name>.paths`, which the inlay itself
tracks. The host ignores every claim through a managed block in
`.git/info/exclude`, and the inlay ignores everything else, so neither
repository sees the other's files. The inlay's git dir lives under the
host's `.git/inlays/`. `git inlay <name> <git command>` runs any git command
against an inlay.

## Orient first

| Command | Answers |
|---|---|
| `git inlay ls` | the claims of every inlay in this worktree |
| `git inlay which <path>...` | the owner of each path: an inlay, `host`, or `none` |
| `git inlay status [--fetch]` | each inlay's branch, ahead/behind, and changed and untracked counts; `--fetch` fetches first |
| `git inlay <name> status`, `git inlay <name> diff` | an inlay's changes in detail |
| `git inlay check` | every invariant; each problem comes with a `fix:` line |

Claimed files do not show in the host's `git status`, `git diff` or
`git add -A`, and search that honours git's ignore rules may skip them.
Open them by path; `git inlay ls` lists them.

## Recording changes

Edit claimed files like any other. To record a change, commit it to the
owning inlay, never to the host:

```sh
git inlay which .claude/settings.json      # -> team
git inlay team add .claude/settings.json
git inlay team commit -m "Allow go test"
git inlay team push
```

A new file under a claimed directory belongs to that inlay; anywhere else
it belongs to the host. A change that touches both is two commits in two
repositories. To move a path into an inlay use `git inlay claim <name>
<path>...`, which also stages it there; `git inlay unclaim <name>
<path>...` gives it back, leaving the file on disk and visible to the host.
Do not edit `.git-inlay/*.paths` or the managed block in
`.git/info/exclude` by hand.

If the host's `.gitignore` already ignores a claimed path, new files under
it need `git inlay <name> add -f <path>`.

## Getting updates

The host and each inlay have separate histories; nothing pins one to the
other. Pull the host first, then each inlay:

```sh
git pull
git inlay status --fetch    # which inlays are behind
git inlay team pull
```

The other order can lose work: when a teammate moves a host file into an
inlay, pulling the inlay before the host's untracking commit makes git
replace the host's copy with the inlay's, uncommitted host edits included.
The pull then warns that `the claims of inlay <name> changed` and lists
the paths out of place, each with a `fix:` line. That fix is a host commit
(`git rm --cached`), which merges cleanly with the teammate's own; confirm
it with the user first.

## Setting up

Confirm with the user before any of these: they create repositories,
choose remotes and push.

```sh
git inlay init team --remote <private url>   # a new, empty inlay
git inlay claim team .claude/settings.json CLAUDE.local.md
git inlay team commit -m "team settings"
git inlay team push -u origin main
git inlay hooks install                       # the host's leak guards
```

On another clone of the host: `git inlay clone <private url> [<name>]`,
then `git inlay hooks install`. In a new linked worktree:
`git inlay attach --all`. After removing worktrees: `git inlay prune`.

An inlay's remote must be private and must never be one of the host's
remotes; git-inlay refuses that. A path the host tracks cannot be claimed
until it is removed from the host with `git rm --cached <path>` and a host
commit, which deletes it upstream for everyone once pushed, so that step is
the user's decision.

## When git-inlay refuses

Read the `fix:` line under each path; it names the command that resolves
it. Never get past a guard with `--no-verify` or `git add -f`.

- `refusing to commit: ... belongs to inlay <name>`: a claimed path is
  staged in the host. Unstage it with `git restore --staged -- <path>`,
  then commit it to the inlay as above.
- `refusing to push: ... belongs to inlay <name>`: an unpushed host commit
  carries a claimed path. It has to come out of those commits, which
  rewrites history; ask the user before doing it. The one exception is
  for the user to judge, not you: commits made while the host still
  tracked the path, before it moved into the inlay, hold nothing private,
  and the refusal names the `git log -p` that shows what they carry.
- `refusing to push inlay <name>`: the push targets one of the host's
  remotes, or the inlay tracks a path outside its claims.
- `refusing to run git clean -x against inlay <name>` (or `-X`, or
  `stash --all`): from an inlay's side every host file is an ignored file,
  so these would act on the whole tree. `git inlay <name> clean -fd` and a
  plain `git inlay <name> stash` stay within the inlay's claims.

Exit status: 0 success, 1 a guard refused or an invariant failed, 2 usage,
3 git failed. `git inlay <name> <git command>` exits with git's own status.

## Keeping the files

In the host, `git clean -x` or `-X` deletes claimed files; prefer
`git clean -fd`, which leaves ignored files alone. After such a deletion,
`git inlay restore` re-checks out what each inlay has committed, and
uncommitted inlay changes are gone. A host `git stash --all` stashes
claimed files with everything else, uncommitted changes included, and
`git stash pop` brings them back; do not run `git inlay restore` before
the pop, or the pop cannot put the stashed files back over the restored
ones.

If a host checkout overwrites a claimed file because upstream added a file
at that path, `git inlay check` reports it, and
`git inlay <name> checkout -- <path>` restores the inlay's copy.
