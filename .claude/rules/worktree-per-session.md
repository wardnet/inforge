---
name: worktree-per-session
---

# Rule: one worktree per agent session

This repository uses a **bare-repo + typed-worktree** layout managed by the
`gt` CLI. Agent sessions in this environment must respect the rules below.

## The rules

1. **Always use the bare-repo layout.** Clone with `gt clone <url>`, never
   plain `git clone`. The expected layout is `<root>/.bare/` plus typed
   worktree directories (`main/`, `feature/`, `fix/`, `chore/`, …).

2. **Sessions start at the gt-managed root** — the directory that is a sibling
   to `.bare/`. Treat that root as a navigation hub only; do NOT edit files
   there and do NOT run commits from it.

3. **Each session must create its own worktree before doing any work.** Use
   `gt wt add <type/name>` to provision a fresh worktree on its own branch,
   then `cd` into it. One session, one worktree, one branch.

   ```sh
   gt wt add feature/<short-task-name>
   cd feature/<short-task-name>
   ```

4. **Whenever a worktree is needed, use `gt`.** Never call `git worktree add`
   or `git worktree remove` directly — raw git bypasses the typed layout and
   the configured worktree-types validation. Use `gt wt add` and `gt wt rm`.

5. **Do not edit inside `.bare/`** and do not delete the default-branch
   worktree (`main/` or equivalent).

6. **Do not use `gt scratch`** — the scratch worktree is reserved for the
   user's manual exploration, not for agent sessions.

## Why

`gt` enforces conventions (typed worktrees, branch naming, direnv-based
`GH_TOKEN` per repo) that the user relies on across many repositories.
Bypassing it produces inconsistent state that the user has to clean up by
hand. The "one worktree per session" rule keeps parallel agent runs from
stepping on each other and keeps every change cleanly attributable to a
named branch.

## See also

- The `use-gt` skill for the detailed command reference and post-clone setup
  behavior.
