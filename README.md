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

or download a release binary and put `git-inlay` on your `PATH`. Git then
exposes it as `git inlay`. `go install` needs Go 1.24 or later. Git 2.40 or
later is required. Release archives
carry a build provenance attestation; check one with
`gh attestation verify <archive> --owner acycl`.

`git inlay -h` prints a summary of the commands. `git inlay --help` is
answered by git itself, which looks for a manual page: release archives
carry one, `man/git-inlay.1`, to copy into a `man1` directory on your
`MANPATH` (`/usr/local/share/man/man1`, say).

The hooks that `git inlay hooks install` writes look for `git-inlay` on
`PATH` first and fall back to the binary that installed them, so commits
from a GUI client that does not see your shell's `PATH` are still guarded.

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
  committed, survives `git clean -xffd`, and needs no exclude entry.
- Claims are recorded in `.git-inlay/<name>.paths`, a file the inlay itself
  tracks, so a clone of the inlay knows what it owns.
- Every command regenerates two exclude files: a managed block in the
  host's `.git/info/exclude` that hides every claim, and the inlay's own
  exclude that hides everything *but* its claims. Neither repository sees
  the other's files. `git inlay <name> <git command>` regenerates them
  again after git exits, so a claim that arrives with
  `git inlay <name> pull` applies at once.
- Claims are disjoint by construction: a path tracked by the host, or
  claimed by another inlay, cannot be claimed.
- `.claude/` can be shared. The host may track `.claude/skills/public/`
  while an inlay claims `.claude/settings.json` and
  `.claude/skills/internal/`; new files go to whichever side owns their
  directory.

## Guards

`init`, `clone` and `attach` add `pre-commit` and `pre-push` shims to the
host wherever its hooks directory has room, unless given `--no-hooks`;
`git inlay hooks install` does the same on request. The first refuses a commit that stages a claimed path (`git add -f`
is the only way to stage one). The second refuses a push whose commits
touch a claimed path, which also catches `--no-verify` commits and
fast-forward merges of such commits. Each inlay carries its own `pre-push`
guard that refuses a push to any of the host's remotes and a push of files
outside its claims.

If a hook manager owns `core.hooksPath`, or a hook that git-inlay did not
write is in the way, nothing is installed unasked and a warning says so.
`hooks install` on git 2.54 or newer adds the guards
as config-defined hooks (`hook.git-inlay-pre-commit` and
`hook.git-inlay-pre-push` in `.git/config`), which git runs beside the
other hooks. On older git it prints the lines to add instead. A GUI client
that bundles git older than 2.54 skips config-defined hooks, so commit
from it only if its git is new enough, or add the printed lines to the
hook manager as well.

`git inlay check` verifies everything at once and exits non-zero on a
problem, naming the path, the owner, and the fix.

## Day to day

| Situation | What happens |
|---|---|
| Getting teammates' changes | `git pull` in the host first, then `git inlay <name> pull` for each inlay; `git inlay status --fetch` shows which are behind. A pulled claim applies at once, and one that covers a path the host still tracks gets a warning with the fix. Pulled the other way round, git replaces the host's copy of a newly claimed file with the inlay's, and uncommitted host edits to it are lost. |
| `git checkout`, `rebase`, `merge`, `stash`, `clean -fd`, `reset --hard` in the host | Nothing. Claimed paths are ignored, so the host leaves them alone. |
| `git clean -fdx` or `git stash --all` in the host | Ignored files go too. `git inlay restore` brings them back; `stash pop` does as well. |
| An upstream commit adds a file at a claimed path | Git overwrites the ignored file on checkout. `git inlay check` reports it; `git rm --cached` plus a commit fixes it, and `git inlay <name> checkout -- <path>` restores the inlay's copy. |
| `git worktree add` | Run `git inlay attach --all` in the new worktree. Claims already bind there through the shared exclude file and hooks. After `git inlay hooks install --auto-attach`, a `post-checkout` hook does it for every new worktree, so the files are in place before a tool or a coding agent starts there. |
| `git worktree remove` | `git inlay prune` removes the worktree's inlays, keeping any with unpushed commits unless forced. |
| `git inlay <name> clean -x` (or `-X`, or `-e '!…'`) or `stash --all`, also behind an alias | Refused. From the inlay's side every host file is an ignored file, so these would act on the whole tree. Run them in the host. |
| The checkout is moved or renamed | The next `git inlay` command other than `check`, which reports it, repairs the absolute paths in each inlay's config. |
| The host's `.gitignore` already ignores a claimed path | The inlay tracks it anyway, but new files under it must be added with `-f`. `claim` warns. |

## Claude Code

Claimed files never show in the host's `git status` or `git diff`, and
search tools that honour git's ignore rules may skip them, so a coding
agent that is not told about the inlays cannot see its edits to them or
know how to commit them. This repository is a Claude Code plugin
marketplace whose `git-inlay` plugin tells it. Install it with:

```
/plugin install git-inlay --marketplace acycl/git-inlay
```

or, on Claude Code before 2.1.275, `/plugin marketplace add acycl/git-inlay`
and then `/plugin install git-inlay@git-inlay`. The plugin needs
`git-inlay` on `PATH`, at a version that has `hook session-start`. It adds:

- a `SessionStart` hook that runs `git-inlay hook session-start`, which
  lists this worktree's claims and how to record changes to them. Outside
  a repository with inlays it prints nothing, so the plugin can stay
  enabled for every project.
- a `git-inlay` skill covering setup, guard refusals and recovery.

A team inlay can enable the plugin for everyone who clones it, through
the `.claude/settings.json` it delivers; Claude Code adds the marketplace
once a person trusts the folder:

```json
{
  "extraKnownMarketplaces": {
    "git-inlay": { "source": { "source": "github", "repo": "acycl/git-inlay" } }
  },
  "enabledPlugins": { "git-inlay@git-inlay": true }
}
```

Other agents with a session-start hook can run
`git-inlay hook session-start` the same way: it reads nothing from stdin
and prints the briefing on stdout.

## Commands

| Command | Does |
|---|---|
| `init <name> [--remote <url>] [--no-hooks]` | create an empty inlay for this worktree |
| `clone <url> [<name>] [--no-hooks]` | attach an inlay from its remote |
| `attach <name>... \| --all [--no-hooks]` | copy inlays from another worktree of the host |
| `detach <name> [--force] [--keep-files]` | remove an inlay from this worktree |
| `claim <name> <path>...` | give paths to an inlay; end a directory that does not exist yet with `/` |
| `unclaim <name> <path>...` | take paths back; the files stay on disk |
| `ls [<name>] [--porcelain]` | list claims |
| `which <path>...` | report who owns each path: an inlay, `host`, or `none` |
| `status [<name>] [--fetch] [--porcelain]` | per inlay: branch, ahead/behind, changed and untracked counts |
| `check [--staged] [--fix]` | verify every invariant; `--fix` first regenerates the exclude files and repairs the inlays' config |
| `restore [<name>]` | re-check out tracked inlay files missing from the tree |
| `prune [--dry-run] [--force]` | remove inlays whose worktree is gone |
| `hooks install [--auto-attach] \| uninstall \| status` | manage the host's guards, which `init`, `clone` and `attach` install where they fit; `--auto-attach` adds a hook that attaches the inlays to every new worktree |
| `<name> <git command> [args]` | run git against an inlay |

Exit status: 0 success, 1 a guard refused or an invariant failed, 2 usage,
3 git failed. `<name> <git command>` exits with git's own status.

## Limitations

- Sparse checkout in the host is unsupported. Claims inside a submodule
  are refused.
- A host `.gitignore` outranks the managed exclude block. A pattern that
  re-includes a claimed path (`!.claude`) cannot be overridden, so such a
  claim is refused.
- The user's global excludes apply to inlays as to any repository. That is
  what keeps Claude Code's `settings.local.json` out of an inlay, and what
  would hide `*.md` from one if the global file ignored Markdown.
- A teammate without git-inlay has none of the files and none of the
  excludes, so nothing to leak, unless they recreate the files by hand.

## Design

`PLAN.md` holds the full design: the on-disk model, the exclude
generation, the invariants, the guards, and a comparison with git-dupe,
sich, overgit, vcsh, and git-sidecar.

## License

Apache-2.0; see `LICENSE`.
