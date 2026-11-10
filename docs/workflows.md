# Day-to-day workflows

## Recording changes

Edit claimed files like any other. To record a change, commit it to the
inlay that owns the file, never to the host:

```sh
git inlay which .claude/settings.json      # -> team
git inlay team add .claude/settings.json
git inlay team commit -m "Allow go test"
git inlay team push
```

A new file under a claimed directory belongs to that inlay; anywhere else
it belongs to the host. A change that touches both sides is two commits in
two repositories.

To see what has changed:

```sh
git inlay status          # a line per inlay
git inlay team status     # one inlay in detail
git inlay team diff
```

## Getting teammates' changes

The host and each inlay have separate histories. Pull the host first, then
each inlay:

```sh
git pull
git inlay status --fetch    # which inlays are behind
git inlay team pull
```

A claim that arrives with the pull applies at once.

The order matters when a teammate has moved a file from the host into an
inlay. Pulled inlay-first, git replaces the host's copy of the file with
the inlay's, and uncommitted host edits to it are lost. The pull then warns
that the claims of the inlay changed and lists the paths out of place,
each with a `fix:` line.

## Moving a file from the host into an inlay

A path the host tracks cannot be claimed. Remove it from the host first:

```sh
git push                                  # publish outstanding host commits
git rm --cached CLAUDE.local.md
git commit -m "Stop tracking CLAUDE.local.md"
git inlay claim team CLAUDE.local.md
git inlay team commit -m "Add CLAUDE.local.md"
```

Things to know before you do this:

- Once the host commit is pushed, the file is deleted upstream for
  everyone. Teammates get it back by pulling the inlay.
- The file remains in the host's history. `claim` warns about that,
  because checking out an old host revision that tracked the path
  overwrites the inlay's copy.
- Pushing the outstanding host commits first avoids a
  [false alarm](guards.md#a-known-false-alarm) from the pre-push guard.

## Giving a path back to the host

```sh
git inlay unclaim team notes/
git inlay team commit -m "Release notes/"
```

The files stay on disk and show up in the host's `git status` as
untracked, ready to be added there or ignored.

## Working in the host

Ordinary host operations leave the inlays' files alone, because the host
ignores them:

| Host operation | Effect on claimed files |
|---|---|
| `checkout`, `switch`, `rebase`, `merge`, `cherry-pick`, `reset --hard` | none |
| `stash`, `stash -u` | none |
| `clean -fd` | none |
| `clean -fdx`, `clean -X` | deleted; see below |
| `stash --all` | stashed with everything else; `stash pop` brings them back |

## Recovering files

**After `git clean -x` in the host.** Run:

```sh
git inlay restore
```

It checks out again the tracked inlay files that are missing from the
tree. Uncommitted inlay changes are gone; only what each inlay had
committed comes back. Prefer `git clean -fd`, which leaves ignored files
alone.

**After `git stash --all` in the host.** Run `git stash pop`, which brings
the files back with their uncommitted changes. Do not run `git inlay
restore` before the pop: the pop cannot put the stashed files back over
the restored ones.

**After a host checkout overwrote a claimed file.** If an upstream commit
adds a file at a claimed path, git overwrites the ignored file on
checkout. `git inlay check` reports it. Untrack the path in the host and
restore the inlay's copy:

```sh
git rm --cached <path>
git commit -m "Stop tracking <path>"
git inlay <name> checkout -- <path>
```

## Moving or renaming the checkout

An inlay's configuration holds absolute paths. After the checkout is moved
or renamed, the next `git inlay` command repairs them. `git inlay check`
alone only reports the problem; `git inlay check --fix` repairs it.

## Removing an inlay

```sh
git inlay detach team
```

removes the inlay from this worktree and deletes its tracked files from
the tree. It stops if there are uncommitted changes, stashes, or commits
on no remote; `--force` overrides that. `--keep-files` leaves the files on
disk, where the host will then see them as untracked.

## Scripting

`ls` and `status` take `--porcelain` for stable, tab-separated output:

```sh
git inlay ls --porcelain        # name<TAB>claim
git inlay status --porcelain    # name, branch, upstream, ahead, behind, changed, untracked
```

`git inlay check` exits 1 on any problem, which suits a CI step or a
script that must not continue with a checkout in a bad state.
