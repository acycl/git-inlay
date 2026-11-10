# Troubleshooting

Start with:

```sh
git inlay check
```

It verifies everything at once and, for each problem, names the path, the
owner, and the fix. `git inlay check --fix` first repairs what git-inlay
can repair by itself (stale exclude files, an inlay configuration left
behind by a moved checkout) and reports what remains.

## Refusals

Every refusal prints a `fix:` line under each path. The common ones:

### `refusing to commit: ... belongs to inlay <name>`

A claimed path is staged in the host, which takes `git add -f`. Unstage it
and commit it to the inlay instead:

```sh
git restore --staged -- <path>
git inlay <name> add <path>
git inlay <name> commit
```

### `refusing to push: ... belongs to inlay <name>`

An unpushed host commit carries a claimed path. It has to come out of
those commits, which means rewriting them.

There is one exception: commits made while the host still tracked the
path, before it moved into the inlay, hold nothing private. The refusal
names the `git log -p` command that shows what the commits carry, and the
one-off `--no-verify` push that is safe once you have looked. See
[a known false alarm](guards.md#a-known-false-alarm).

### `refusing to push inlay <name>`

Either the push targets one of the host's remotes, or the inlay tracks a
path outside its claims. For the second, claim the path or remove it from
the inlay's commits.

### `refusing to run git clean -x against inlay <name>`

Also for `clean -X`, `clean -e '!...'` and `stash --all`. From the inlay's
side every host file is an ignored file, so these would act on the whole
tree. Use `git inlay <name> clean -fd` or a plain `git inlay <name>
stash`, or run the command in the host.

### `<path> is tracked by the host`

A path the host tracks cannot be claimed. See
[moving a file from the host into an inlay](workflows.md#moving-a-file-from-the-host-into-an-inlay).

### `<path> is already owned by inlay <name>`

Claims of different inlays cannot overlap, in either nesting direction.
`git inlay which <path>` names the owner; `unclaim` it there first.

### `<path> is re-included by ...`

A `.gitignore` pattern such as `!.claude` re-includes the path, and
`.gitignore` files outrank the exclude file git-inlay manages, so the host
could never be made to ignore it. Remove the pattern or choose another
path.

### `clone` refuses because files are in the way

`clone` and `attach` never overwrite a file at a path the inlay would
write. Move the listed files aside and run the command again.

## Warnings

### `host leak guards are not active`

A hook manager or a hook of the host's own occupies the hooks directory.
Run `git inlay hooks install`; see
[Guards](guards.md#beside-a-hook-manager-or-an-existing-hook).

### `the host does not ignore <claim>`

A `.gitignore` pattern outranks the managed exclude block, so the hooks
are the only guard for that path. `git inlay check` shows the details.

### `the claims of inlay <name> changed, and N path(s) are out of place`

A pull brought in a claim that does not fit this host: it overlaps
another inlay's claim, or covers a path the host still tracks. This is
usually the result of pulling an inlay before the host; see
[getting teammates' changes](workflows.md#getting-teammates-changes).
Each path comes with its fix.

## Symptoms

| Symptom | Cause and remedy |
|---|---|
| The inlay's files are gone after `git clean -fdx` | They were ignored files to the host. `git inlay restore` brings back what was committed. |
| The files are gone after `git stash --all` | They are in the stash. `git stash pop`, and do not run `git inlay restore` first. |
| A new file under a claimed directory does not show in `git inlay <name> status` | The host's `.gitignore`, or your global excludes file, ignores it. Add it with `git inlay <name> add -f <path>`. |
| A new worktree has no inlay files | Run `git inlay attach --all` there, or install the [auto-attach hook](worktrees.md#attaching-automatically). |
| `git inlay check` reports an inlay whose worktree is gone | `git inlay prune`. |
| Commits or pushes fail with `git-inlay: binary not on PATH` | The hook cannot find the binary. Put `git-inlay` back on `PATH`, or run `git inlay hooks uninstall` if you have stopped using it. |
| A GUI client commits a claimed path without complaint | Its bundled git is older than 2.54 and skips config-defined hooks. See [Guards](guards.md#beside-a-hook-manager-or-an-existing-hook). |
| The Claude Code plugin's session-start hook fails with an unknown hook error | The installed `git-inlay` predates `hook session-start`. Upgrade the binary. |
| `git inlay --help` says there is no manual entry | Install `man/git-inlay.1`, or use `git inlay -h`. |

## Limitations

- Sparse checkout in the host is unsupported; `init` and `clone` refuse
  it.
- Claims inside a submodule of the host are refused.
- Claims are explicit files or directories. There are no globs.
- A host `.gitignore` outranks the managed exclude block. A pattern that
  re-includes a claimed path cannot be overridden, so such a claim is
  refused.
- The user's global excludes apply to inlays as to any repository.
- Checking out host history that tracked a now-claimed path overwrites the
  inlay's copy of the file. `git inlay <name> checkout -- <path>` restores
  it.
- Hook shims need a POSIX `sh`, which Git for Windows ships.
- The guards can be bypassed on purpose; see
  [what the guards do not cover](guards.md#what-the-guards-do-not-cover).
