# Worktrees

## The model

Each worktree of the host has its own copy of each inlay, with its own
index, HEAD and branch, just as each worktree has its own checkout of the
host. The copies live under the host's shared git directory:

```
.git/inlays/main/<name>/      for the primary worktree
.git/inlays/wt/<id>/<name>/   for the linked worktree <id>
```

Three things are shared by all worktrees, because they live in the common
git directory:

- the managed block in `.git/info/exclude`, which lists the claims of every
  inlay attached to any worktree;
- the host's hooks, so the guards run everywhere;
- the repository configuration.

So in a new worktree the claims already bind: the claimed paths are
ignored and guarded there. What the worktree lacks is the files.

## Attaching inlays to a new worktree

```sh
git worktree add ../my-project-feature
cd ../my-project-feature
git inlay attach --all
```

`attach` copies inlays from another worktree of the host: the branch they
have there is checked out, and the same remote is configured. Name
specific inlays instead of `--all` to take only those.

Only commits travel. Uncommitted changes in the other worktree are
reported, not copied. Like `clone`, `attach` never overwrites a file that
is already in place.

## Attaching automatically

Where worktrees are made often, or something starts in them at once (a
coding agent reads `.claude/` when its session begins), install the
auto-attach hook once:

```sh
git inlay hooks install --auto-attach
```

This adds a `post-checkout` hook that runs `attach --all` in each worktree
that `git worktree add` creates and checks out, so the files are in place
before anything else runs there.

- Worktrees made with `--no-checkout` or `--orphan` run no hook; run
  `git inlay attach --all` in them yourself.
- The hook is a convenience, not a guard. It never fails the checkout: a
  failed attach is a warning that names `git inlay attach --all`.
- `git inlay hooks status` lists it, and `git inlay hooks uninstall`
  removes it with the guards.

## Removing a worktree

`git worktree remove` does not delete the worktree's inlays, which live in
the common git directory. Their claims keep binding every worktree until
they are pruned, and `git inlay check` reports them. Clean up with:

```sh
git inlay prune --dry-run    # what would be removed
git inlay prune
```

An inlay with commits on no remote, or with stashes, is kept, and `prune`
prints the push command that publishes its commits. `--force` removes it
anyway.

An orphaned inlay is also a source for `attach`: if the worktree is gone
but its inlay had unpushed commits, attaching the inlay in another
worktree recovers that history.
