# git-inlay

Keep private sets of files tracked in git and present in a repository's
working tree, without ever committing them to that repository.

An **inlay** is a second git repository whose working tree is the host's.
It owns a set of paths, its **claims**; the host ignores those paths through
`.git/info/exclude`; and hooks on both sides stop anything from crossing
over. Typical inlays: a team's Claude Code settings on an open-source
project, one person's research notes in a monorepo, local tooling config
that must sit in place but not ship.

## Install

```sh
go install github.com/acycl/git-inlay/cmd/git-inlay@latest
```

or download a [release](https://github.com/acycl/git-inlay/releases) binary
and put `git-inlay` on your `PATH`. Git then exposes it as `git inlay`.
`go install` needs Go 1.24 or later. Git 2.40 or later is required.
[Getting started](docs/getting-started.md) has the details, including the
manual page and how to verify a release archive.

## Quick start

```sh
cd my-project
git inlay init team --remote git@github.com:me/my-project-team.git
git inlay claim team .claude/settings.json .claude/skills/internal/ CLAUDE.local.md
git inlay team commit -m "team settings"
git inlay team push -u origin main
```

On another machine, or for a teammate:

```sh
git clone git@github.com:org/my-project.git && cd my-project
git inlay clone git@github.com:me/my-project-team.git
```

The files are now in place, the host's `git status` is clean, `git add -A`
stages none of them, and `git inlay team ...` runs any git command against
the inlay.

## How it works

- The inlay's git dir lives under the host's `.git/inlays/`, so it is never
  committed and survives `git clean -xffd`.
- Claims are recorded in `.git-inlay/<name>.paths`, a file the inlay itself
  tracks, so a clone of the inlay knows what it owns.
- Every command regenerates two exclude files: one makes the host ignore
  every claim, the other makes the inlay ignore everything *but* its
  claims. Neither repository sees the other's files.
- Claims are disjoint by construction: a path tracked by the host, or
  claimed by another inlay, cannot be claimed.
- `pre-commit` and `pre-push` guards in the host refuse a commit or a push
  that carries a claimed path, and each inlay refuses a push to any of the
  host's remotes.

## Commands

| Command | Does |
|---|---|
| `init <name> [--remote <url>]` | create an empty inlay for this worktree |
| `clone <url> [<name>]` | attach an inlay from its remote |
| `attach <name>... \| --all` | copy inlays from another worktree of the host |
| `detach <name>` | remove an inlay from this worktree |
| `claim <name> <path>...` | give paths to an inlay |
| `unclaim <name> <path>...` | take paths back; the files stay on disk |
| `ls [<name>]` | list claims |
| `which <path>...` | report who owns each path: an inlay, `host`, or `none` |
| `status [<name>] [--fetch]` | per inlay: branch, ahead/behind, changed and untracked counts |
| `check [--fix]` | verify every invariant, naming the path, the owner, and the fix |
| `restore [<name>]` | re-check out tracked inlay files missing from the tree |
| `prune` | remove inlays whose worktree is gone |
| `hooks install \| uninstall \| status` | manage the host's guards |
| `<name> <git command> [args]` | run git against an inlay |

`git inlay -h` prints this summary; the
[command reference](docs/commands.md) has every option and the exit
statuses.

## Documentation

| Page | Covers |
|---|---|
| [Getting started](docs/getting-started.md) | installing, a first inlay, bringing a teammate on board |
| [Concepts](docs/concepts.md) | hosts, inlays, claims, the exclude files, the on-disk layout |
| [Command reference](docs/commands.md) | every command, its options, output, and exit status |
| [Guards](docs/guards.md) | what stops a leak, and working beside husky, lefthook, or the pre-commit framework |
| [Day-to-day workflows](docs/workflows.md) | recording and pulling changes, moving files in and out, recovering deleted files |
| [Worktrees](docs/worktrees.md) | `git worktree`, automatic attach, pruning |
| [Claude Code and other agents](docs/claude-code.md) | the plugin and the session-start briefing that tell a coding agent about the inlays |
| [Troubleshooting](docs/troubleshooting.md) | refusals, warnings, and limitations |
| [Design](docs/design.md) | the full design record: the on-disk model, the exclude generation, the invariants, the guards, and a comparison with git-dupe, sich, overgit, vcsh, and git-sidecar |

## License

Apache-2.0; see `LICENSE`.
