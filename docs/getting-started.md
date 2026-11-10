# Getting started

## Requirements

- Git 2.40 or later.
- Go 1.24 or later, only if you install with `go install`.
- On Windows, the `sh` that Git for Windows ships, which runs the hooks.

## Install

With Go:

```sh
go install github.com/acycl/git-inlay/cmd/git-inlay@latest
```

Or download a release archive from the
[releases page](https://github.com/acycl/git-inlay/releases) and put
`git-inlay` on your `PATH`. Archives are built for Linux, macOS and Windows
on amd64 and arm64, and carry a build provenance attestation:

```sh
gh attestation verify <archive> --owner acycl
```

Git runs any `git-<name>` program on `PATH` as `git <name>`, so the tool is
used as `git inlay`. Check the installation with:

```sh
git inlay version
```

### Help and the manual page

`git inlay -h` prints a summary of the commands. `git inlay --help` is
answered by git itself, which looks for a manual page. Release archives
carry one, `man/git-inlay.1`; copy it into a `man1` directory on your
`MANPATH`, such as `/usr/local/share/man/man1`.

## Create your first inlay

An inlay needs a name and, to be shared, a remote of its own. The remote
must be a different repository from any of the host's remotes; git-inlay
refuses one that is the same.

```sh
cd my-project
git inlay init team --remote git@github.com:me/my-project-team.git
```

`init` creates an empty inlay for this worktree and installs the host's
[guards](guards.md) where the hooks directory has room. Now give it some
paths:

```sh
git inlay claim team .claude/settings.json .claude/skills/internal/ CLAUDE.local.md
```

`claim` records the paths as the inlay's claims and stages the files in the
inlay. A path that does not exist yet is claimed as a file unless it ends
with a slash, so write `notes/` to claim a directory you have not created.

Commit and push through the inlay. `git inlay <name>` followed by any git
command runs that command against the inlay:

```sh
git inlay team commit -m "team settings"
git inlay team push -u origin main
```

From here on:

- the files are in place in the working tree, for every tool that reads it;
- the host's `git status` is clean and `git add -A` stages none of them;
- `git inlay team status`, `diff`, `log`, and so on show the inlay's side.

## Bring a teammate on board

On another machine, or for a teammate, clone the host as usual and then
clone the inlay into it:

```sh
git clone git@github.com:org/my-project.git && cd my-project
git inlay clone git@github.com:me/my-project-team.git
```

`clone` reads the inlay's claims from its remote, checks them against this
host, and checks the files out. It never overwrites a file that is already
at a claimed path: the clone is refused and names the files to move aside.

## Check that everything is in order

```sh
git inlay ls       # the claims of every inlay in this worktree
git inlay status   # each inlay's branch, distance from upstream, and changes
git inlay check    # every invariant; exits non-zero on a problem
```

## Next steps

- [Concepts](concepts.md) explains what just happened.
- [Day-to-day workflows](workflows.md) covers recording and pulling changes.
- [Guards](guards.md) matters if the host uses husky, lefthook, the
  pre-commit framework, or hooks of its own.
- [Claude Code and other agents](claude-code.md) matters if a coding agent
  works in the checkout.
