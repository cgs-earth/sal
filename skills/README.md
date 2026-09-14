# Agent skills

Skills that teach a coding agent (Claude Code, Codex, or anything else that reads the
[Agent Skills](https://agentskills.io) `SKILL.md` format) how to work with SAL:

- [`sal/`](sal/SKILL.md): create, validate, build, run, query, and publish a SAL project.
- [`salmodule/`](salmodule/SKILL.md): write, test, and publish a SAL module.

## Install

As a Claude Code plugin, from the marketplace this repository publishes:

```
/plugin marketplace add cgs-earth/sal
/plugin install sal@sal
```

With the `skills` CLI, into whichever agents it finds on the machine (Claude Code, Codex, and others):

```sh
npx skills add cgs-earth/sal
```

By hand, into a project or a user profile:

```sh
# Claude Code
git clone --depth 1 https://github.com/cgs-earth/sal /tmp/sal
cp -r /tmp/sal/skills/sal /tmp/sal/skills/salmodule .claude/skills/      # or ~/.claude/skills/

# Codex
cp -r /tmp/sal/skills/sal /tmp/sal/skills/salmodule .codex/skills/       # or ~/.codex/skills/
```

Or pin them as a git submodule so `git submodule update --remote` picks up new versions:

```sh
git submodule add https://github.com/cgs-earth/sal .claude/skills/sal-upstream
```
