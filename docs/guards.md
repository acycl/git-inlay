# Guards

Ignore rules keep claimed files out of the host by default, but `git add
-f` can still stage one. The guards are what refuse a leak before it is
published.

## What is guarded

| Guard | Where it runs | Refuses |
|---|---|---|
| claim time | `claim`, `clone`, `attach` | a claim on a path the host tracks, or one that overlaps another inlay's claim |
| pre-commit | host hook | a commit that stages a claimed path, or anything under `.git-inlay/` |
| pre-push | host hook | a push whose commits touch a claimed path |
| inlay pre-push | a hook in each inlay's git directory | a push to any of the host's remotes, and a push of files outside the inlay's claims |
| remote check | `init`, `clone`, `attach`, `check` | an inlay remote that is the same repository as a host remote |

The host's pre-push guard is the backstop for the pre-commit guard: it
scans the commits being pushed, so it also catches commits made with
`--no-verify` and fast-forward merges of branches that carry a claimed
path. Staged deletions never block a commit.

Every refusal lists the offending paths, names the owning inlay, and prints
the command that resolves it on a `fix:` line.

## Installing the host guards

`init`, `clone` and `attach` install the `pre-commit` and `pre-push` guards
wherever the host's hooks directory has room, and say so. Pass `--no-hooks`
to decline. They never touch a hook manager's directory or a hook that
git-inlay did not write; in that case they warn that the guards are not
active and name the command to run:

```sh
git inlay hooks install
```

The hooks directory and the repository configuration are shared by linked
worktrees, so one install covers all of them.

### The shim

Where the hooks directory is free, the guard is a small shell script:

```sh
#!/bin/sh
# >>> git-inlay >>>
bin=$(command -v git-inlay) || bin='/home/me/go/bin/git-inlay'
[ -x "$bin" ] || { echo "git-inlay: binary not on PATH or at $bin; refusing. ..." >&2; exit 1; }
exec "$bin" hook pre-push "$@"
# <<< git-inlay <<<
```

It looks for `git-inlay` on `PATH` first and falls back to the binary that
installed it, so commits from a GUI client that does not see your shell's
`PATH` are still guarded. With neither in place it fails closed: the commit
or push is refused.

### Beside a hook manager or an existing hook

If `core.hooksPath` is set, as husky does, or a hook that git-inlay did
not write is already in place, as with lefthook and the pre-commit
framework, which write their own into the hooks directory, the shim cannot
go there. What `hooks install` does then depends on the git version.

**Git 2.54 or newer.** It adds the guards as config-defined hooks in the
host's `.git/config`:

```ini
[hook "git-inlay-pre-push"]
    command = ...
    event = pre-push
    enabled = true
```

Git runs these beside the other hooks, whatever `core.hooksPath` says, and
the commit or push fails if either refuses.

**Older git.** It writes nothing and prints what to add by hand. Where it
finds a hook manager's files in the repository, those are the lines for
that manager's configuration; for any other hook already in place, it is
the line to add to that hook. The pre-push guard needs the arguments git
gives the hook, and the refs git writes to its stdin, which is why the
snippets differ.

husky:

```
.husky/pre-commit:  git-inlay hook pre-commit
.husky/pre-push:    git-inlay hook pre-push "$@"
```

lefthook, in `lefthook.yml`:

```yaml
pre-commit:
  commands:
    git-inlay:
      run: git-inlay hook pre-commit
pre-push:
  commands:
    git-inlay:
      run: git-inlay hook pre-push {0}
      use_stdin: true
```

The pre-commit framework, in `.pre-commit-config.yaml`:

```yaml
- repo: local
  hooks:
    - id: git-inlay-pre-commit
      name: git-inlay leak guard
      entry: git-inlay hook pre-commit
      language: system
      pass_filenames: false
      always_run: true
      stages: [pre-commit]
    - id: git-inlay-pre-push
      name: git-inlay leak guard
      entry: git-inlay hook pre-push
      language: system
      pass_filenames: false
      always_run: true
      stages: [pre-push]
```

and install its pre-push hook, which `pre-commit install` alone leaves
out:

```sh
pre-commit install --hook-type pre-commit --hook-type pre-push
```

Any other hook: run `git-inlay hook pre-commit` from the pre-commit hook,
and `git-inlay hook pre-push "$@"` from the pre-push hook with git's stdin
forwarded.

Note that these snippets go into files the host tracks, so adding them is a
change to the host that its maintainers must accept.

> A GUI client or IDE that bundles a git older than 2.54 skips
> config-defined hooks. Commit and push from it only if its git is new
> enough, or add the snippet to the hook manager as well.

## Hook states

`git inlay hooks status` prints one line per hook with its state:

| State | Meaning |
|---|---|
| `installed` | git-inlay's shim is in the hooks directory |
| `configured` | git-inlay's config-defined hook is in effect (git 2.54 or newer) |
| `inert` | the shim is there but not executable, so git skips it |
| `disabled` | the config-defined hook has `enabled = false` |
| `managed` | `core.hooksPath` is set, so a hook manager owns the hooks |
| `foreign` | a hook is present that git-inlay did not write |
| `absent` | there is no hook |

Only `installed` and `configured` run a guard. `git inlay check` reports
every other state as a problem when `hooks install` could fix it. Below git
2.54, a `managed` or `foreign` hook cannot be inspected, so `check` warns
that it cannot verify the guard instead.

`git inlay hooks install` repairs an `inert` shim and re-enables a
`disabled` hook. If the hook stays `disabled`, the setting comes from
configuration that git reads after the repository's, and the command
prints how to find it:

```sh
git config --show-scope --show-origin --get-all hook.git-inlay-pre-push.enabled
```

## Removing the guards

```sh
git inlay hooks uninstall
```

removes the shims and the `hook.git-inlay-*` configuration sections, and
the auto-attach hook if it was installed.

## What the guards do not cover

The guards protect against slips, not against someone determined to get
past them.

- **`--no-verify`** on both the commit and the push skips the host hooks.
  The inlay-side guards still apply.
- **`-c core.hooksPath=/dev/null`** skips the shims in the hooks
  directory. A config-defined guard still runs; it can be switched off
  with `-c hook.git-inlay-<hook>.enabled=false`.
- **`git clean -x` and `git stash --all` in the host** remove ignored
  files, the inlays' included. Nothing leaks, but the files leave the
  tree; see [recovering files](workflows.md#recovering-files).
- **A teammate without git-inlay** has none of the files and none of the
  excludes, so has nothing to leak, unless they recreate the files by
  hand.
- **Raw git with `--git-dir`** against an inlay bypasses the refusal of
  `clean -x` and `stash --all`, as do shell aliases (`!...`) and
  `help.autocorrect` rewrites.

## A known false alarm

The pre-push guard sees paths, not intent. If the host once tracked a file
that has since moved into an inlay, host commits made before the move
still touch that path, and pushing them afterwards is refused. Those
commits hold nothing private. The refusal names the `git log -p` command
that shows what the commits carry, and the one-off `--no-verify` push that
is then safe. To avoid it, push the host's outstanding commits before
claiming a path the host used to track.
