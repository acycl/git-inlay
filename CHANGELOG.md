# Changelog

What changed for users in each release, newest first. Versions follow
[semantic versioning](https://semver.org); before 1.0 a minor release may
change behaviour.

## Unreleased

### Changed

- `-h` after a command prints that command's usage. A mistyped command,
  inlay name or option is answered with what was typed, what was probably
  meant, and the usage of the command.

### Fixed

- `git inlay <name> clean` and `stash -u` are refused while the inlay
  sees host files as untracked, which a host `.gitignore` pattern that
  re-includes them causes; `clean -fd` used to delete such files. `check`
  warns about them, and `detach` no longer takes them for uncommitted work.
- The remote guard recognizes the host's repository behind a different
  port (`https://host/a/b` and `ssh://git@host:2222/a/b`), behind an
  `insteadOf` alias that does not look like a URL, and through a symbolic
  link; a relative path given to `init --remote` or `clone` is read from
  the current directory.
- The lefthook snippet passes the pre-push hook's arguments, without which
  every push of a new branch scanned the whole history. The snippet for a
  hook manager is printed for lefthook and the pre-commit framework too,
  and a pre-commit framework push that carries a root commit is guarded.
- Paths in the `fix:` lines of a refusal are quoted so that a shell reads
  them as plain text, whatever they hold.
- `detach` leaves the files the inlay tracked outside its claims, which
  are most likely the host's.
- A manifest that does not parse no longer stops `git inlay <name> ...`,
  which is how it is repaired. Merge conflict markers and control
  characters in a manifest are refused, where they used to pass for claims.
- A symbolic link is claimed as a file, also when it points at a directory.
- `unclaim` works on a file edited since it was claimed; `restore` brings
  the exclude files up to date; `init` refuses to overwrite a manifest
  already in the tree.
- A worktree made where another was removed gets its inlays' files back.
- The guards see a nested repository staged under `diff.ignoreSubmodules`
  and a leaked commit papered over with `git replace`; sparse checkout is
  detected however the setting is spelled.
- A shim the user added lines to is left alone; duplicate managed blocks
  in `.git/info/exclude` are collapsed.
- `claim`, `unclaim` and `which` work from a directory typed in another
  case than the one on disk, on a case-insensitive filesystem.

## 0.3.0 - 2026-11-09

### Added

- `init`, `clone` and `attach` install the host's guards where the hooks
  directory has room; `--no-hooks` declines.
- `hooks install --auto-attach` attaches the inlays to every new worktree.
- `check --fix` repairs what the sync step can.
- Warnings when a pull changes an inlay's claims in a way that breaks an
  invariant, and when an inlay comes to track a path outside its claims.
- The manual page, shipped in the release archives.

### Fixed

- The inlay's push guard also refuses a path outside the claims in the
  commits on the way to the tip.
- `git inlay <name> ...` outlives an interrupt as git does, and ends by it
  when git did.
- The exclude files are regenerated after a passthrough command.
- `claim` and `detach` handle more files than a command line carries.
- A name is held back for future commands only from new inlays.

## 0.2.0 - 2026-11-06

### Added

- `git inlay hook session-start`, a briefing on the worktree's inlays for
  a coding agent's session-start hook.
- A Claude Code plugin, with that hook and a `git-inlay` skill.

## 0.1.0 - 2026-11-06

First release: `init`, `clone`, `attach`, `detach`, `claim`, `unclaim`,
`ls`, `which`, `status`, `check`, `restore`, `prune`, `hooks`, and
`git inlay <name> <git command>`.
