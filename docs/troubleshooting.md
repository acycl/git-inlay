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

If the message says the command would take `host file(s) the inlay does
not ignore`, a host `.gitignore` re-includes those files (`!.env.example`),
which outranks the inlay's exclude file, so `clean` and `stash -u` would
reach them. Remove the inlay's own untracked files by hand, or run the
command the refusal prints once you have checked what it would take with
`git inlay <name> clean -n`.

### `.git-inlay/<name>.paths line N: ...`

The inlay's manifest does not parse: a merge left conflict markers in it,
a pull brought a line that is not a valid path, or a hand edit went wrong.
No claim can be trusted without it, so every command stops, the host's
guards included. The exception is git run against that inlay, which is
how you put it right:

```sh
git inlay <name> status                               # what state is it in?
git inlay <name> checkout -- .git-inlay/<name>.paths  # undo a hand edit
git inlay <name> merge --abort                        # give up a merge that conflicted
git inlay <name> reset --hard ORIG_HEAD               # undo a pull that brought the bad line
```

The last discards uncommitted changes to the inlay's files, so commit or
stash them first. To resolve a conflict instead of abandoning the merge,
edit the file, keep the claims of both sides, remove the marker lines, and
`git inlay <name> add` it.

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

### `no inlay named "<name>" is attached to this worktree`

The first word after `git inlay` that is not a command is read as an
inlay's name, so this is also what a mistyped command says; where the
word is close to a command or to an inlay here, the message suggests it.
Otherwise the inlay is not in this worktree: `git inlay ls` shows the ones
that are, and in a new worktree `git inlay attach --all` brings over the
rest.

### `<url> is the host's remote <name>`

An inlay must have a repository of its own. Given the host's, `init
--remote` and `clone` refuse, since a push of the inlay would put the
private files exactly where they must not go. Create a separate, empty
repository for the inlay.

### `<url> is not an inlay repository`

`clone` found no `.git-inlay/<name>.paths` on the remote's default branch.
The URL names some other repository, or an inlay that was never pushed;
`the remote has no branches` means the latter. Push it first, from where
it was made, with `git inlay <name> push -u origin main`.

### `inlay <name> now tracks N path(s) outside its claims`

A pull or checkout brought in a commit that carries a file the inlay does
not claim, and wrote it into the tree, over the host's file if one was
there. `git status` in the host shows what was overwritten; `git checkout
-- <path>` there gets it back. Then take the path out of the inlay, or
claim it, as the `fix:` line of each path says.

### `inlay <name> has uncommitted changes`, `has N stash(es)`, `has N commit(s) on no remote`

`detach` and `prune` stop before work would be lost. Commit and push it,
or pass `--force` to give it up. `prune` prints the push that clears each
inlay it kept.

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
| Commits or pushes fail with `git-inlay: binary not on PATH` | The hook cannot find the binary. Put `git-inlay` back on `PATH`. If you have stopped using it and the binary is gone, remove the guards by hand; see [uninstalling](getting-started.md#uninstalling). |
| A GUI client commits a claimed path without complaint | Its bundled git is older than 2.54 and skips config-defined hooks. See [Guards](guards.md#beside-a-hook-manager-or-an-existing-hook). |
| The Claude Code plugin's session-start hook fails with `unknown hook "session-start"` | The installed `git-inlay` is 0.1.0, which predates the hook. Upgrade to 0.2.0 or later. |
| `git inlay --help` says there is no manual entry | Install `man/git-inlay.1`, or use `git inlay -h`. |

## Limitations

- Sparse checkout in the host is unsupported; `init`, `clone` and
  `attach` refuse it.
- Claims inside a submodule of the host are refused.
- Claims are explicit files or directories. There are no globs.
- A host `.gitignore` outranks the managed exclude block. A pattern that
  re-includes a claimed path cannot be overridden, so such a claim is
  refused. A pattern that re-includes a host file shows that file to an
  inlay as untracked; `check` warns, and the inlay's `clean` and
  `stash -u` are refused while such files exist.
- The user's global excludes apply to inlays as to any repository.
- Checking out host history that tracked a now-claimed path overwrites the
  inlay's copy of the file. `git inlay <name> checkout -- <path>` restores
  it.
- Hook shims need a POSIX `sh`, which Git for Windows ships.
- The guards can be bypassed on purpose; see
  [what the guards do not cover](guards.md#what-the-guards-do-not-cover).
