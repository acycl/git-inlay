# Claude Code and other agents

## The problem

Claimed files never show in the host's `git status`, `git diff` or
`git add -A`, and search tools that honour git's ignore rules may skip
them. A coding agent that is not told about the inlays edits files it
cannot see in any diff and has no way to learn how to commit them.

A note in the host's `CLAUDE.md` cannot help: the host is usually public
and unaware of git-inlay, and the claims differ per checkout.

## The session-start briefing

```sh
git-inlay hook session-start
```

prints a briefing meant for an agent's session-start hook, whose output
the agent takes as context. It covers:

- each attached inlay and its claims (up to 20 per inlay);
- how to inspect changes to them and commit through the owning inlay;
- what not to do: `git add -f` on a claimed path, `--no-verify`;
- the host operations that delete or stash claimed files;
- inlays that only other worktrees hold;
- host guards that are not active, if any.

Outside a git working tree, or in a repository without inlays, it prints
nothing and exits 0, so one hook can serve every project. It reads nothing
from stdin and writes nothing to the repository.

## The Claude Code plugin

This repository is a Claude Code plugin marketplace with one plugin,
`git-inlay`. Install it with:

```
/plugin install git-inlay --marketplace acycl/git-inlay
```

or, on Claude Code before 2.1.275:

```
/plugin marketplace add acycl/git-inlay
/plugin install git-inlay@git-inlay
```

The plugin needs `git-inlay` 0.2.0 or later on `PATH`, the first version
with `hook session-start`. It adds:

- a `SessionStart` hook that runs `git-inlay hook session-start` at
  the start of every session, however it starts (new, resumed, forked,
  or after a clear or a compaction), so the briefing stays current;
- a `git-inlay` skill with the procedures: setup, inspecting and recording
  changes, reading guard refusals, and recovery.

Because the briefing is empty where there are no inlays, the plugin can
stay enabled for every project.

## Enabling the plugin for a team

A team inlay can turn the plugin on for everyone who clones it, through
the `.claude/settings.json` it delivers:

```json
{
  "extraKnownMarketplaces": {
    "git-inlay": { "source": { "source": "github", "repo": "acycl/git-inlay" } }
  },
  "enabledPlugins": { "git-inlay@git-inlay": true }
}
```

Claude Code adds the marketplace once a person trusts the folder. The
entry turns the plugin on but does not download it, so each person still
installs it once per clone:

```sh
claude plugin install git-inlay@git-inlay --scope project
```

## Delivering Claude Code configuration through an inlay

This is the use git-inlay was built for: team settings on a project whose
repository cannot or should not carry them.

```sh
git inlay init team --remote git@github.com:me/my-project-team.git
git inlay claim team .claude/settings.json .claude/skills/internal/ CLAUDE.local.md
git inlay team commit -m "team settings"
git inlay team push -u origin main
```

- The files are real files in place, so Claude Code loads them as it
  would any project configuration.
- The host can keep tracking its own public parts of `.claude/`, such as
  `.claude/skills/public/`, beside the inlay's.
- Claude Code adds `settings.local.json` to the user's global excludes,
  which apply to inlays too, so personal local settings stay out of the
  team inlay.

## Worktrees and agents

An agent that starts in a fresh worktree reads `.claude/` at once. Install
the auto-attach hook so the inlays' files are there before it does:

```sh
git inlay hooks install --auto-attach
```

See [Worktrees](worktrees.md#attaching-automatically).

## Other agents

Any agent with a session-start hook can run `git-inlay hook session-start`
the same way and add its stdout to the agent's context.
