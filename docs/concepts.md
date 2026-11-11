# Concepts

The model behind git-inlay, the terms it uses, and where each piece lives
on disk.

## The model in one paragraph

An **inlay** is a second git repository whose working tree is the host's.
It owns a set of paths, its **claims**. The host ignores those paths, the
inlay ignores everything else, and hooks on both sides stop anything from
crossing over. Each file in the tree therefore belongs to exactly one
repository, and each repository behaves as if the other were not there.

## Terms

| Term | Meaning |
| --- | --- |
| host | the repository whose working tree holds the inlay's files: the one you cloned |
| inlay | a git repository with its own history and remote, attached to one worktree of the host |
| claim | a path, relative to the root of the worktree, that an inlay owns; a directory claim ends with `/` and owns everything beneath it |
| manifest | an inlay's list of claims, `.git-inlay/<name>.paths`, which the inlay itself tracks |
| common dir | the host's shared git directory, `.git` in an ordinary clone; all worktrees of the host share it |

## Where things live

```text
.git/inlays/main/<name>/        the inlay's git directory, for the primary worktree
.git/inlays/wt/<id>/<name>/     the same, for the linked worktree <id>
.git/info/exclude               the host's exclude file, with a managed block
.git-inlay/<name>.paths         the manifest, tracked by the inlay
```

The inlay's git directory sits inside the host's `.git`, so it is never
committed, needs no ignore rule, survives `git clean -xffd`, and is not
deleted by `git worktree remove`. The only footprint in the working tree is
the `.git-inlay/` directory, which the host ignores.

An inlay is a plain git repository. git-inlay creates it bare, then sets
`core.bare` to false and `core.worktree` to the host's worktree, so raw git
with `--git-dir` works against it too. It also points `core.hooksPath` at
the inlay's own hooks directory, where its push guard lives, so that no
hook manager's setting in your global configuration switches the guard
off.

## Claims and the manifest

The manifest is a text file with one claim per line, kept sorted:

```text
# git-inlay manifest for "claude". Edit with: git inlay claim claude <path>
.claude/
docs/research/
packages/foo/.claude/
```

Because the inlay tracks its own manifest, a clone of the inlay knows what
it owns, and a teammate's new claim arrives with `git inlay <name> pull`.
Change the manifest with `git inlay claim` and `git inlay unclaim`, not by
hand.

Rules for a claim:

- It names one file or one directory. There are no globs.
- A directory claim ends with `/`. A path that does not exist when it is
  claimed is a file unless you give it a trailing slash. The kind stays as
  recorded; claim the path again to change it.
- It cannot be under `.git/` or `.git-inlay/`, or inside a submodule of the
  host.
- It cannot be a path the host tracks, or contain one.
- It cannot equal, contain, or sit inside a claim of another inlay.

On a filesystem that ignores case, `claim` spells a path as the disk does
and the host-side checks match paths against claims with case folded, so
renaming `.claude` to `.Claude` does not carry a file past them.

## How the two repositories stay apart

Two generated exclude files do the work. Every git-inlay command except
`version`, `check` without `--fix`, and the internal `hook` family
regenerates them from the manifests first, so they are always current.

**The host's managed block.** In `.git/info/exclude`, a block lists every
claim of every inlay attached to any worktree:

```text
# >>> git-inlay managed block; do not edit >>>
/.git-inlay
/.claude
/docs/research
/packages/foo/.claude
# <<< git-inlay managed block <<<
```

Text outside the markers is yours and is preserved byte for byte.

**The inlay's exclude file.** In the inlay's own `info/exclude`, a chain of
patterns ignores everything, then re-includes each claim and the
directories leading to it:

```text
/*
!/.git-inlay/
/.git-inlay/*
!/docs/
/docs/*
!/packages/
/packages/*
!/packages/foo/
/packages/foo/*
!/.claude/
!/.git-inlay/claude.paths
!/docs/research/
!/packages/foo/.claude/
```

The result is that plain `git status` is correct on both sides: the host
shows none of the inlay's files, and the inlay shows none of the host's.

`git inlay <name> <git command>` regenerates the files a second time after
git exits, so a claim that arrives with a pull applies at once.

## Sharing a directory

Claims are files or directories at any depth, so the host and an inlay can
share a directory. The host may track `.claude/skills/public/` while an
inlay claims `.claude/settings.json` and `.claude/skills/internal/`. A new
file goes to whichever side owns the directory it is created in.

## Invariants

`git inlay check` verifies four invariants, and the [guards](guards.md)
enforce them as you work:

| | Invariant | Meaning |
| --- | --- | --- |
| I1 | Disjointness | no path is both tracked by the host and claimed by an inlay, and no path is claimed by two inlays |
| I2 | Exclusion | the host ignores every claim |
| I3 | Containment | every file an inlay tracks is under one of its claims, or is its manifest |
| I4 | Remote separation | no inlay remote is the same repository as a host remote |

Remote comparison looks through spelling: `git@host:a/b`,
`ssh://git@host/a/b` and `https://host/a/b` are the same repository, with
or without a trailing `.git`, and git's `insteadOf` rewrites are applied.
A port sets two URLs apart only when both name one for the same transport,
so `https://host/a/b`, `git@host:a/b` and `ssh://git@host:2222/a/b` are
still one repository. What the comparison cannot see through is a name
that only your machine resolves: a `Host` alias in `~/.ssh/config`, or a
second hostname for the same server.

## What git-inlay does not do

- It does not override files the host tracks. A path belongs to one
  repository.
- It does not encrypt anything. Access control is the inlay remote's job.
- It does not wrap everyday git. There is no `git inlay commit` or `sync`;
  you run git itself against the inlay with `git inlay <name> ...`.
- It does not pin an inlay to a host revision. The two histories are
  independent.

## Other ignore rules

Git's ignore sources have a fixed precedence: `.gitignore` files outrank
`info/exclude`, which outranks the user's global excludes file. Three
consequences:

- **A host `.gitignore` that already ignores a claimed path** hides new
  files under that claim from the inlay's `git status`. Tracked files are
  unaffected. `claim` warns, and new files there need
  `git inlay <name> add -f <path>`.
- **A host `.gitignore` that re-includes a claimed path** (`!.claude`)
  cannot be overridden by the managed block, so such a claim is refused.
- **A host `.gitignore` that re-includes a host file** (`!.env.example`)
  outranks the inlay's exclude file too, so the inlay sees that file as
  untracked when it sits at the root of the tree or directly inside a
  directory that leads to a claim. A pattern that re-includes a whole
  directory there (`!/src/`) shows the inlay everything in it. `git inlay check`
  warns about such
  files. Keep them out of `git inlay <name> add`; `git inlay <name> clean`
  and `stash -u`, which would take them, are refused while any exist.

The user's global excludes apply to an inlay as to any repository. That is
what keeps Claude Code's `settings.local.json` out of a team inlay; it is
also what would hide `*.md` from an inlay if your global file ignored
Markdown.
