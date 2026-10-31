# git-inlay: implementation plan

`git-inlay` keeps sets of files version-controlled and physically present in a
git repository's working tree without ever committing them to that repository.
Each set is an *inlay*: a plain git repository of its own, with its own history
and remote, whose working tree is the host repository's working tree.

Target uses: a team's `.claude/` configuration on an open-source project, and
personal research notes kept next to the code they concern.

Status: plan, 2026-10-31. Nothing is implemented yet.

## 1. Goals and non-goals

Goals:

1. Inlay files are real files in place. Tools that walk the tree see them.
2. The host repository never sees inlay paths: not in `git status`, not in
   `git add -A`, and not in anything pushed.
3. Several inlays can coexist over one host, each with its own remote.
4. A teammate gets an inlay onto a fresh host clone with one command.
5. Leaks are blocked before publication, not detected after.
6. Linked worktrees of the host work.
7. The inlay is a boring git repository. Any git command works on it.

Non-goals (deliberately out of scope):

- Overriding files the host tracks. A path belongs to exactly one repository.
- Encryption. Access control is the inlay remote's job.
- Glob claims. Claims are explicit files or directories; a directory claim
  covers everything beneath it.
- Wrapping everyday git. No `commit`, `pull`, `push`, or `sync` verbs.
- Private content whose correctness depends on the host revision. That is the
  job of an export tool such as Copybara.

## 2. Prior art and what we take from it

| Tool | Taken | Left |
|---|---|---|
| git-dupe | state inside the host `.git`; explicit `GIT_DIR`/`GIT_WORK_TREE`; managed block in `info/exclude`; `git dupe clone` onboarding | dual tracking with a warning; no hooks; per-worktree state deleted by `git worktree remove`; argument-based remote guard |
| sich | ownership across several layers; negation-chain exclude for the inlay; tracked `.paths` manifest; pre-commit guard | state in the tree; everyday-git wrappers; no pre-push guard; no linked worktrees |
| overgit | tracked JSON manifest as the portable description | the override model entirely |
| vcsh | the "fake bare" repository: `git init --bare` then `core.bare=false` | everything else |
| git-sidecar | unified storage at `.git/git-sidecar/<nick>/gitdir`; several named sidecars, each with its own URL; managed exclude block; name-less passthrough when inside a sidecar's directory | whole-directory mappings only, so private and host files cannot share a directory; per-user config outside the repo, so nothing is shared through git; no ownership checks, no hooks, no worktree handling |

New in git-inlay: disjointness enforced at claim time, a pre-push guard that
scans outgoing commits, remote comparison on resolved URLs, hook shims designed
to be called from hook managers, per-worktree inlay clones that survive
worktree removal, and a restore verb instead of intercepting `clean` and `stash`.

## 3. Terminology

- **host**: the repository whose working tree holds the inlay files.
- **common dir** (`$C`): `git rev-parse --git-common-dir` of the host. Shared by
  all worktrees of the host.
- **worktree key**: `main` for the host's primary worktree; `wt/<id>` for the
  linked worktree whose git dir is `$C/worktrees/<id>`.
- **inlay**: a git repository named `<name>` attached to one host worktree.
- **claim**: a root-relative path that an inlay owns. A directory claim ends
  with `/` and owns everything beneath it.
- **manifest**: the inlay's list of claims, tracked by the inlay.

## 4. On-disk model

### 4.1 Layout

```
$C/inlays/main/<name>/            inlay git dir for the primary worktree
$C/inlays/wt/<id>/<name>/         inlay git dir for linked worktree <id>
$C/info/exclude                     host exclude file, carries the managed block
<host root>/.git-inlay/<name>.paths   manifest, tracked by inlay <name>
```

Inlay git dirs live under the common dir, so they are never committed, need no
exclude entry of their own, survive `git clean -xffd`, and are not deleted by
`git worktree remove`. The `main` and `wt/` segments cannot collide because they
sit at different depths.

The only footprint in the tree is `.git-inlay/`, which the host excludes.
Each inlay implicitly claims its own manifest file.

### 4.2 Inlay repository configuration

Created with `git init --bare` then configured:

```
core.bare = false
core.worktree = <absolute path of the host worktree>
inlay.name = <name>
inlay.host = <absolute common dir>
```

`core.worktree` is a courtesy for people who open the inlay with raw git. The
tool itself always passes `--git-dir` and `--work-tree` as absolute paths and
scrubs inherited `GIT_*` variables (section 7.1).

`status.showUntrackedFiles` is left at its default. The inlay's exclude file
(section 4.4) makes plain `git status` correct.

### 4.3 Manifest format

`.git-inlay/<name>.paths`, UTF-8, one claim per line, kept sorted:

```
# git-inlay manifest for "claude". Edit with: git inlay claim claude <path>
.claude/
docs/research/
packages/foo/.claude/
```

Claim validation, applied by `claim` and when reading a manifest:

- root-relative, forward slashes, no leading `/`, no `.` or `..` segments
- directories end with `/`, files do not; the filesystem is checked at claim time
- no newline or carriage return, no leading `#`, no leading or trailing space
- not under `.git/`, `.git-inlay/`, or inside a submodule of the host
- not equal to or under a path the host tracks (section 6, I1)
- not equal to, under, or above a claim of another inlay (I1)
- a claim under another claim of the same inlay is dropped as redundant,
  with a warning

### 4.4 Generated exclude files

**Host managed block** in `$C/info/exclude`, regenerated at the start of every
command from the union of all inlays attached to any worktree:

```
# >>> git-inlay managed block; do not edit >>>
/.git-inlay
/.claude
/docs/research
/packages/foo/.claude
# <<< git-inlay managed block <<<
```

Entries carry no trailing slash so they match a file or a directory of that
name, which protects against a claimed directory being replaced by a file.
Text outside the markers is preserved byte for byte.

**Inlay exclude** in `<inlay git dir>/info/exclude`, generated from that
inlay's claims. The algorithm:

1. Emit `/*`.
2. Collect every proper ancestor directory of every claim, including those of
   the implicit manifest claim. Sort by depth, then by name. For each `d` emit
   `!/d/` then `/d/*`.
3. For each claim `c`, sorted, emit `!/c` (file) or `!/c/` (directory).

Example for the manifest above:

```
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

Claim negations come last, so they win over the `/d/*` rules. Every parent is
re-included before its children are excluded, which satisfies git's rule that a
file cannot be re-included when a parent directory is excluded.

Escaping, in both files: a backslash before `#`, `!`, `*`, `?`, `[`, `\`, and a
trailing space in any path segment.

The user's global excludes file still applies to inlays, as it does to any
repository. That is desirable for Claude Code, which adds
`**/.claude/settings.local.json` to the global excludes. A global rule such as
`*.md` would hide research notes from the inlay; document this.

## 5. Command surface

The binary is `git-inlay`, so git exposes it as `git inlay`. Paths on the
command line are relative to the current directory and are normalized against
`git rev-parse --show-prefix`.

| Command | Behaviour |
|---|---|
| `git inlay init <name> [--remote <url>]` | create an empty inlay for this worktree; refuse a remote that matches a host remote |
| `git inlay clone <url> [<name>]` | create the inlay from a remote, read its manifest, check claims against the host, check out; `<name>` defaults to the URL's basename |
| `git inlay attach <name>` | in a linked worktree: create the inlay from the sibling in another worktree, pointing its remote at the real URL |
| `git inlay detach <name> [--force] [--keep-files]` | remove the inlay git dir; refuse with uncommitted or unpushed work unless forced; delete the inlay's tracked files unless `--keep-files` |
| `git inlay claim <name> <path>...` | validate, add to manifest, regenerate excludes, stage manifest and paths in the inlay |
| `git inlay unclaim <name> <path>...` | remove from manifest, `git rm --cached` in the inlay, regenerate; warn that the files are now visible to the host |
| `git inlay ls [<name>]` | list claims, per inlay |
| `git inlay which <path>...` | report the owner of each path: host, an inlay, or none |
| `git inlay status [--fetch]` | short status of every inlay in this worktree, plus ahead/behind |
| `git inlay check [--staged]` | run every invariant from section 6; non-zero exit on any violation |
| `git inlay restore [<name>]` | re-check out tracked inlay files that are missing from the tree, for example after `git clean -x` |
| `git inlay prune [--dry-run]` | delete inlay git dirs whose host worktree no longer exists; refuse those with unpushed commits unless forced |
| `git inlay hooks install\|uninstall\|status` | manage the host hook shims (section 7.3) |
| `git inlay hook pre-commit\|pre-push\|inlay-pre-push` | internal; what the shims call |
| `git inlay <name> <git args...>` | passthrough: run git against the inlay; `<name>` cannot be a verb above |
| `git inlay version` | print version and the git version in use |

Every command except `version` and the `hook` family begins with a sync step:
read the manifests of all attached inlays, regenerate the host managed block
and this worktree's inlay excludes, and verify I1 and I2. The hook family
reads manifests but does not write.

For inlays attached in other worktrees, the manifest is read from the
inlay's HEAD with `git show HEAD:.git-inlay/<name>.paths`, so the union is
correct even when that worktree is on disk elsewhere.

## 6. Invariants and guards

- **I1 Disjointness.** No path is both tracked by the host and claimed by an
  inlay. No path is claimed by two inlays, including by nesting.
- **I2 Exclusion.** Every claim is excluded in the host. Verified with
  `git check-ignore` after writing the block.
- **I3 Containment.** Every file an inlay tracks is under one of its claims
  or is its manifest.
- **I4 Remote separation.** No inlay remote resolves to the same repository
  as any host remote.

Guards:

| Guard | Where | Checks |
|---|---|---|
| claim time | `claim`, `clone`, `attach` | I1 against `git ls-files -z -- <claims>` in the host and against other inlays' manifests |
| pre-commit | host hook | staged paths (`git diff --cached --name-only -z --diff-filter=ACMR`) under any claim or under `.git-inlay/`; staged deletions never block |
| pre-push | host hook | for each ref update on stdin: skip deletions; range is `<remote-oid>..<local-oid>`, or `<local-oid> --not --remotes=<remote>` when the remote oid is zero; `git rev-list` piped to `git diff-tree -r -m --stdin --no-commit-id --name-only -z`; fail on any path under a claim |
| inlay pre-push | hook in each inlay git dir | I4 on the destination URL; I3 on the outgoing commits |
| remote config | `init`, `clone`, `attach`, `check` | I4 on every configured remote |

The pre-push input is accepted in three forms: the stdin lines git provides, the
`PRE_COMMIT_FROM_REF`, `PRE_COMMIT_TO_REF` and `PRE_COMMIT_REMOTE_NAME`
variables set by the pre-commit framework, and explicit `--from`/`--to` flags.

Remote comparison normalizes URLs before comparing: scp-like `git@host:a/b`,
`ssh://git@host/a/b`, and `https://host/a/b` compare equal; a trailing `.git`
and trailing slashes are ignored; `insteadOf` rewrites are applied by asking
git with `git ls-remote --get-url <remote>`.

Every guard failure lists the offending paths, names the owning inlay, and
prints the exact command that resolves it.

What guards do not cover, and the documented answer:

- `git clean -x` and `git stash -a` in the host remove ignored files from the
  tree. `git inlay restore` brings them back; `stash pop` also restores them.
- `--no-verify` on both commit and push bypasses the hooks. The inlay
  pre-push guard and the remote check still apply to the inlay side.
- A teammate without git-inlay installed has none of the files and none of
  the excludes, so has nothing to leak unless they recreate files by hand.

## 7. Implementation

### 7.1 Package layout

```
cmd/git-inlay/main.go      entry point; calls internal/cli
internal/cli/                one file per verb; flag parsing with the standard flag package
internal/gitx/               git subprocess runner
internal/host/               host discovery: toplevel, common dir, worktree key, hooks dir, remotes
internal/inlay/            inlay lifecycle: create, open, clone, attach, detach, enumerate
internal/manifest/           parse, validate, format, sort
internal/exclude/            host managed block; inlay negation chain; escaping
internal/guard/              I1 to I4 checks shared by claim, check, and the hooks
internal/hooks/              shim content, install, uninstall, hook-manager detection
internal/remote/             URL normalization and comparison
internal/cli/cli_test.go     end-to-end scenarios against the built binary and real git
```

`gitx.Runner` holds the git dir, work tree, and working directory. It builds
every command as `git --git-dir=<abs> --work-tree=<abs> ...`, sets `LC_ALL=C`,
and removes `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`,
`GIT_OBJECT_DIRECTORY`, `GIT_ALTERNATE_OBJECT_DIRECTORIES`, `GIT_COMMON_DIR`
and `GIT_NAMESPACE` from the environment. It offers `Run`, `Output`, and
`OutputZ` (NUL-separated lines) and wraps failures with the command line and
stderr. Passthrough uses the same runner with stdin, stdout, and stderr
connected to the terminal.

Dependencies: the standard library only, at runtime and in tests. No go-git.

### 7.2 Creating and attaching inlays

`init`:

1. Resolve the host (toplevel, common dir, worktree key). Refuse outside a
   host worktree or inside a bare repository.
2. Refuse a name that is a verb, contains `/`, or is already attached here.
3. `git init --bare -b main $C/inlays/<key>/<name>`, then set the config of
   section 4.2 and write the inlay exclude.
4. Write the manifest with only the header, stage it, and make the initial
   commit.
5. If `--remote` is given, check I4, then `git remote add origin <url>`.
6. Install the inlay pre-push hook in the new git dir.
7. Regenerate the host block and run `hooks status`; print a reminder if the
   host hooks are not installed.

`clone` runs steps 1 to 3, then `git fetch origin`, reads the manifest from
`origin/HEAD`, validates every claim against the host (I1) and against other
inlays, and then checks out with `git checkout` without `-f`, so git refuses
to overwrite untracked host files at claimed paths. That refusal is surfaced
with the paths and a hint to move them aside.

`attach` is `clone` with the sibling inlay's git dir as the fetch source, the
remote URL copied from the sibling, and the sibling's current branch checked
out.

### 7.3 Host hooks

`hooks install`:

- If `core.hooksPath` is set, a hook manager owns the hooks. Print the snippet
  for the detected manager (husky from `.husky/`, lefthook from `lefthook.yml`,
  the pre-commit framework from `.pre-commit-config.yaml`, otherwise generic)
  and write nothing.
- Otherwise, for `pre-commit` and `pre-push` in `git rev-parse --git-path
  hooks`: if the file is absent, write the shim; if it contains our marker,
  rewrite it; if it exists without our marker, write nothing and print the
  line to add. Chaining a pre-push hook by appending is unsafe because both
  scripts would read stdin.

Shim:

```sh
#!/bin/sh
# >>> git-inlay >>>
if command -v git-inlay >/dev/null 2>&1; then
  exec git-inlay hook pre-push "$@"
fi
echo "git-inlay: binary not on PATH; refusing. Run 'git inlay hooks uninstall' to remove this hook." >&2
exit 1
# <<< git-inlay <<<
```

The shim fails closed. The host hooks directory is shared by linked worktrees,
so one install covers all of them.

`hooks status` reports, per hook, one of: installed, managed by
`core.hooksPath` (snippet printed, verify by hand), present without our
marker, or absent.

### 7.4 Worktrees

Inlay clones are per host worktree, keyed under `$C/inlays/`. Each has its
own index, HEAD, and branch, which matches the mental model of a worktree. Git's
own linked-worktree mechanism is not used for inlays because the host already
owns the `.git` file at the worktree root. Objects are duplicated between
sibling clones; inlay content is small, so this is accepted.

`prune` lists `$C/inlays/wt/<id>/` directories whose `$C/worktrees/<id>` is
gone, reports unpushed commits per inlay, and deletes only with
confirmation or `--force`.

### 7.5 Error handling and output

- Exit codes: 0 success, 1 invariant violation or guard refusal, 2 usage, 3
  git failure.
- Human output on stdout, diagnostics on stderr, no color by default,
  `--porcelain` on `ls`, `which`, and `status` for scripting.
- Every refusal names the path, the owner, and the fix.

## 8. Testing

Unit tests, table-driven, in each package:

- manifest: parse, format, sort, round trip, every validation rule
- exclude: golden files for the host block and the negation chain, including
  escaping and nested claims
- remote: URL normalization pairs that must and must not compare equal
- guard: pre-push stdin parsing, zero-oid handling, range construction

Integration tests with real git in `internal/cli`, one test function per
scenario, each building a host repository, a local bare remote for the host,
and local bare remotes for inlays. The binary is built once in TestMain and
driven as `git inlay ...`, so git's subcommand lookup and the hook shims are
exercised:

1. init, claim, commit; host status is clean; `git add -A` stages nothing.
2. `git add -f` of a claimed file is blocked by pre-commit; with `--no-verify`
   the push is blocked by pre-push; after a fast-forward merge of a branch
   that contains a claimed path, the push is still blocked.
3. clone into a second host clone restores the files and the exclude block.
4. claim of a host-tracked path is refused; overlapping claims across two
   inlays are refused in both nesting directions.
5. inlay remote equal to a host remote is refused in ssh, scp-like, and
   https spellings.
6. linked worktree: attach, independent commits, host exclude union, prune
   after `git worktree remove`.
7. `git clean -xfd` then restore.
8. `core.hooksPath` set: install prints the snippet and writes nothing.
9. detach with and without `--keep-files`.
10. passthrough runs with the right git dir and refuses a verb as a name.

CI: GitHub Actions on ubuntu, macos, and windows with the runner's git, plus
one Linux job on a container with the oldest supported git. Supported floor:
git 2.40, chosen for `--git-common-dir` and worktree stability; raise it if a
needed feature requires more. macOS runs exercise a case-insensitive
filesystem.

Verification subagents per the project conventions: `go-test-runner` and
`go-vet-runner` after each change; `code-reviewer` before each milestone is
declared done.

## 9. Milestones

| # | Deliverable | Done when |
|---|---|---|
| M0 (done 2026-10-31) | Mechanics spike as standard-library tests in `internal/gitx/mechanics_test.go`, driving raw git with no production code | tests prove: bare-init-plus-config inlay works for status, add, commit, checkout with explicit dirs; the common dir's exclude applies in a linked worktree; `git clean -x` leaves `.git` untouched; the negation chain re-includes a nested `.claude/`; a host exclude without a trailing slash matches a file or a directory; `git worktree add` refuses a non-empty directory |
| M1 (done 2026-10-31) | Core | `init`, `claim`, `unclaim`, `ls`, `which`, passthrough; manifest and both exclude generators; scenarios 1, 4, 10 |
| M2 (done 2026-10-31) | Remotes | `clone`, remote normalization, I4; scenarios 3, 5 |
| M3 (done 2026-10-31) | Guards | `check`, `hook pre-commit`, `hook pre-push`, inlay pre-push, `hooks install\|uninstall\|status`; scenarios 2, 8 |
| M4 | Worktrees | worktree keys, `attach`, `prune`, union exclude; scenario 6 |
| M5 | Lifecycle and release | `detach`, `restore`, `status`, `--porcelain`, README, goreleaser config, CI matrix; scenarios 7, 9 |

M0 is done before any Go beyond the runner is written. If a mechanic fails,
the design changes before code depends on it. The M0 tests stay as regression tests for the git behaviour the design
relies on. M1 to M3 were built on 2026-10-31 with standard-library tests
only; testscript was dropped because a small fixture type covers the same
ground without a dependency.

## 10. Risks and open questions

- **Case-insensitive filesystems.** Git sets `core.ignoreCase` at init by
  probing the directory. The inlay is initialized under `$C`, normally on
  the same filesystem as the tree. Verify that exclude matching honours it for
  both repositories; git-dupe emits every ASCII case variant, which suggests
  it found a gap.
- **Windows.** Hook shims need the `sh` that Git for Windows ships.
  `core.worktree` with a drive letter and forward slashes must be tested.
  Symlinks are not involved, which helps.
- **First push of a new inlay branch** scans with `--not --remotes`, which
  can be slow on a large host when the remote has no refs locally. Measure;
  cap with a warning if needed.
- **Sparse checkout and submodules** in the host are unsupported in v1.
  Claims inside a submodule are refused; sparse checkout is detected and
  refused at `init` and `clone`.
- **Name.** Decided 2026-10-31: `git-inlay`, after a registry search found
  `git-overlay` taken at least seven times and `inlay` clear everywhere. The
  repository and module path were renamed to `github.com/acycl/git-inlay`
  the same day.
- **Manifest in the tree versus in a ref.** A tracked file is simple and
  diffable at the cost of one ignored directory at the root. Storing it in a
  dedicated ref would give a pristine root but needs custom refspecs on every
  fetch and push. Start with the file; revisit only if the directory proves
  to be a problem.
- **Global excludes leaking into inlays.** Inherit by default, document the
  effect, and consider an `inlay.excludesFile` override later.
- **Host `.gitignore` files outrank the inlay's exclude file.** gitignore
  precedence is the command line, then `.gitignore` files, then
  `info/exclude`, then the global excludes. A host that already ignores
  `.claude/` therefore hides new files under that claim from the inlay's
  status; tracked files are unaffected. `claim` stages with `-f` after
  applying the inlay's own rules and the global excludes itself, and warns.
  `clone` checks for collisions before checkout, because git overwrites
  ignored files silently.
- **Host checkouts of history that tracked a claimed path** overwrite the
  inlay's files with the host's copy, because the path is ignored by the
  host. `claim` warns when the path appears anywhere in the host's history.
