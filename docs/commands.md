# Command reference

```
git inlay init <name> [--remote <url>] [--no-hooks]
git inlay clone <url> [<name>] [--no-hooks]
git inlay attach (<name>... | --all) [--no-hooks]
git inlay detach <name> [--force] [--keep-files]
git inlay claim <name> [--] <path>...
git inlay unclaim <name> [--] <path>...
git inlay ls [<name>] [--porcelain]
git inlay which <path>...
git inlay status [<name>] [--fetch] [--porcelain]
git inlay check [--staged] [--fix]
git inlay restore [<name>]
git inlay prune [--dry-run] [--force]
git inlay hooks (install [--auto-attach] | uninstall | status)
git inlay <name> <git command> [<args>...]
git inlay version
```

## Conventions

- Options may appear anywhere on the command line; `--` ends them. An
  unknown option is a usage error, never a path.
- `-h` or `--help` after a command prints the usage summary and does
  nothing else. `git inlay --help` is answered by git, which shows the
  manual page.
- `claim`, `unclaim` and `which` read paths relative to the current
  directory. A path that starts with `:(top)` is relative to the root of
  the worktree instead.
- Results go to stdout and diagnostics to stderr. There is no color.
- Every command except `version` and `check` without `--fix` first
  regenerates the exclude files from the claims (see
  [Concepts](concepts.md#how-the-two-repositories-stay-apart)).

## Exit status

| Status | Meaning |
|---|---|
| 0 | success |
| 1 | a guard refused, or an invariant failed |
| 2 | usage error |
| 3 | git failed |

`git inlay <name> <git command>` exits with git's own status.

## Setting up inlays

### init

```
git inlay init <name> [--remote <url>] [--no-hooks]
```

Creates an empty inlay for this worktree, on branch `main`, with an initial
commit that holds its manifest. With `--remote`, the URL becomes the
inlay's `origin`; a URL that is one of the host's remotes is refused.

Unless `--no-hooks` is given, `init` installs the host's
[guards](guards.md) wherever the hooks directory has room.

A name cannot contain `/`, cannot be a git-inlay command, and cannot be one
of the names held back for future commands: `all`, `completion`, `doctor`,
`each`, `exec`, `fetch`, `list`, `mv`, `pull`, `push`, `repair`, `show`,
`sync`.

A host that uses sparse checkout is refused.

### clone

```
git inlay clone <url> [<name>] [--no-hooks]
```

Attaches an inlay from its remote and checks out its files, on the remote's
default branch. The inlay's name comes from its manifest; `<name>`, when
given, must match it, so the clone of a repository that holds a different
inlay is refused.

Before checking out, `clone` validates every claim against this host and
against the inlays already attached. A file already at a path the inlay
would write is never overwritten: the clone is refused and lists the files.

### attach

```
git inlay attach (<name>... | --all) [--no-hooks]
```

Copies inlays attached in another worktree of the host into this one, on
the branch they have there and with the same remote. `--all` takes every
inlay not yet attached here. Only commits travel: uncommitted changes in
the other worktree are reported, not copied. See [Worktrees](worktrees.md).

### detach

```
git inlay detach <name> [--force] [--keep-files]
```

Removes an inlay from this worktree, and its tracked files from the tree
unless `--keep-files` is given. A file the host also tracks is never
deleted. Uncommitted changes, stashes, and commits on no remote stop the
command unless `--force` is given.

## Claims

### claim

```
git inlay claim <name> [--] <path>...
```

Gives paths to an inlay: validates them, adds them to the manifest,
regenerates the exclude files, and stages the manifest and the files in the
inlay. Commit afterwards with `git inlay <name> commit`.

A path that the host tracks, that another inlay claims, or that lies inside
a submodule is refused. A path that does not exist yet is claimed as a file
unless it ends with a slash. A claim under another claim of the same inlay
is dropped as redundant, with a warning.

### unclaim

```
git inlay unclaim <name> [--] <path>...
```

Takes paths back from an inlay. The files stay on disk and become visible
to the host as untracked.

### ls

```
git inlay ls [<name>] [--porcelain]
```

Lists claims, one `<name><TAB><claim>` line each. An inlay without claims
is shown as `(no claims)`, except with `--porcelain`, which prints claim
lines only.

### which

```
git inlay which <path>...
```

Prints `<path><TAB><owner>` for each path, where the owner is an inlay's
name, `host`, or `none`.

## Inspecting

### status

```
git inlay status [<name>] [--fetch] [--porcelain]
```

Prints one line per inlay in this worktree, or for the one named: its
branch, its distance from its upstream, and a summary of changed and
untracked files. `--fetch` fetches each inlay's remote first; if a fetch
fails, the last known state is shown with a warning.

With `--porcelain`, each line holds seven tab-separated fields:

```
name  branch  upstream  ahead  behind  changed  untracked
```

`branch` is `detached` for a detached HEAD, and `upstream` is empty when
the branch has none.

### check

```
git inlay check [--staged] [--fix]
```

Verifies every invariant and exits 1 on a problem. Each problem names the
path, the owner, and the command that fixes it. On success it prints a line
such as:

```
ok: 2 inlay(s), 5 claim(s), nothing out of place
```

`check` writes nothing, so it can see a hand edit of an exclude file and
runs against a read-only `.git`. With `--fix`, it first regenerates the
exclude files and repairs the inlays' configuration, then reports what
remains. `--staged` checks only what is staged in the host, which is what
the pre-commit guard runs.

Besides the four [invariants](concepts.md#invariants), `check` reports
stale exclude files, host hooks that run no guard, claims whose kind
(file or directory) no longer matches the disk, inlays whose worktree is
gone, and a checkout that has been moved.

## Recovery and cleanup

### restore

```
git inlay restore [<name>]
```

Checks out again the tracked inlay files that are missing from the tree,
as after `git clean -x` in the host. Files that are present are left
alone. Only committed content comes back.

### prune

```
git inlay prune [--dry-run] [--force]
```

Removes the inlays of worktrees that no longer exist, and the staging
directories that an interrupted `clone` or `attach` left behind. Inlays
with commits on no remote, or with stashes, are kept unless `--force` is
given; for each, `prune` prints the push command that clears it. There is
no interactive confirmation; use `--dry-run` to see what would happen.

## Hooks

### hooks

```
git inlay hooks (install [--auto-attach] | uninstall | status)
```

Manages the host's guards. See [Guards](guards.md) for the full behaviour.

- `install` writes the `pre-commit` and `pre-push` guards. Where a hook
  manager owns `core.hooksPath`, or a hook of the host's own is in the way,
  it adds them as config-defined hooks on git 2.54 or newer, and otherwise
  prints the lines to add by hand. `--auto-attach` also adds a
  `post-checkout` hook that attaches the inlays to each new worktree.
- `uninstall` removes everything `install` wrote.
- `status` prints, per hook, its state and where it lives, followed by one
  line per inlay for its own pre-push guard.

### hook

`git inlay hook <event>` is what the installed hooks call. The events
`pre-commit`, `pre-push`, `inlay-pre-push` and `post-checkout` are
internal. One is meant to be called directly:

```
git inlay hook session-start
```

It prints a briefing on this worktree's inlays for a coding agent's
session-start hook, or nothing where there are none. See
[Claude Code and other agents](claude-code.md).

## Running git against an inlay

```
git inlay <name> <git command> [<args>...]
```

Runs any git command against the inlay, in the current directory, with the
terminal attached. The exit status is git's own.

```sh
git inlay team status
git inlay team diff
git inlay team add .claude/settings.json
git inlay team commit -m "Allow go test"
git inlay team pull
git inlay team log --oneline
```

After git exits, git-inlay regenerates the exclude files again, since a
pull, merge, checkout or reset can change the manifest. If the inlay's
claims changed, it warns about any that overlap another inlay's or that
the host tracks or does not ignore. It also warns when the command brought
in a tracked path that lies outside the inlay's claims.

Three forms are refused, also behind a git alias, because from the inlay's
side every host file is an ignored file and they would act on the whole
tree:

- `clean -x`, `clean -X`, and `clean -e '!...'`
- `stash --all` (or `-a`)

`git inlay <name> clean -fd` and a plain `git inlay <name> stash` stay
within the inlay's claims. Run the others in the host.

One case narrows that. Where a host `.gitignore` re-includes a host file
that the inlay would otherwise ignore (see
[other ignore rules](concepts.md#other-ignore-rules)), the inlay sees the
file as untracked, and `clean` (except with `-n`) and `stash -u` are
refused while such files exist. The refusal names them.

## version

```
git inlay version
```

Prints the version of git-inlay and of git. `git inlay --version` does the
same.
