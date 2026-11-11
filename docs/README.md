# git-inlay documentation

git-inlay keeps private sets of files tracked in git and present in a
repository's working tree, without ever committing them to that repository.

## Guides

- [Getting started](getting-started.md) — install git-inlay, create a
  first inlay, and bring a teammate on board
- [Day-to-day workflows](workflows.md) — record changes, get teammates'
  changes, move files in and out of an inlay, and recover deleted files
- [Worktrees](worktrees.md) — use inlays with `git worktree`, attach them
  automatically, and prune them
- [Claude Code and other agents](claude-code.md) — tell a coding agent
  about the inlays in a checkout

## How it works

- [Concepts](concepts.md) — understand hosts, inlays, claims, the exclude
  files, and where everything lives on disk
- [Guards](guards.md) — see what stops a private file from reaching the
  host's history, and how the guards work beside a hook manager

## Reference

- [Command reference](commands.md) — look up a command, its options, its
  output, and its exit status
- [Troubleshooting](troubleshooting.md) — read a refusal or a warning, and
  check the known limitations

The manual page is [`man/git-inlay.1`](../man/git-inlay.1), and the
[changelog](../CHANGELOG.md) lists what each release changed.

## For contributors

- [Design](design.md) — the design record: the reasoning behind each
  decision, the internals, the testing approach, and a comparison with
  similar tools

Comments in the code cite the design's sections by number, so keep the
numbering stable when editing it.
