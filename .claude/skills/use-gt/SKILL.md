---
name: use-gt
description: |
  Use the `gt` CLI for any clone, worktree, or branch operation in this environment. Trigger when the user asks you to clone a repo, start work on a branch, create or remove a worktree, or when you find yourself in a bare-repo layout (a directory containing `.bare/` plus typed worktree dirs like `main/`, `feature/`, `fix/`, `chore/`). Prefer `gt clone` over `git clone`, and `gt wt add` / `gt wt rm` over raw `git worktree` commands.
---

# use-gt

This skill instructs agents to manage repos, worktrees, and branches through
the `gt` CLI instead of raw `git` commands. `gt` enforces a bare-repo +
typed-worktree layout that the user relies on, and it wires up direnv-based
GitHub auth per repo.

## Step 1 — Ensure `gt` is installed

Before running any `gt` command, make sure the binary is available:

```sh
./agentic/skills/use-gt/scripts/ensure-gt.sh
```

Run from anywhere by using the absolute path to the script. It is idempotent —
it exits early if `gt` is already on PATH, otherwise it runs the official
installer (`https://raw.githubusercontent.com/pedromvgomes/gt/main/install.sh`).

If the script reports the binary is not on PATH after install, instruct the
user to add `$HOME/.local/bin` to PATH and re-run.

To upgrade an existing install non-interactively:

```sh
gt update --check     # report whether an update is available
gt update --yes       # install the latest release
```

## Step 2 — One worktree per session

When you start a session inside an existing gt-managed root (sibling to
`.bare/`), do **not** edit files at the root or inside the default-branch
worktree directly. Create your own worktree for the session and `cd` into it:

```sh
gt wt add feature/<short-task-name>
cd feature/<short-task-name>
```

This keeps every agent session isolated on its own branch and worktree, which
is the workflow the user expects. See also `agentic/rules/worktree-per-session.md`.

## Step 3 — Cloning a new repo

Always use `gt clone` instead of `git clone`. It produces the bare-repo layout
this workflow expects:

```sh
gt clone <url> [folder]
```

**Before cloning, ask the user which GitHub user to authenticate as.** The
post-clone direnv setup wires `GH_TOKEN` to a specific `gh` user, and the
choice is sticky for the lifetime of that clone. Show the current default
(`gh auth status` lists the active user) and ask whether to use it or a
different one. Example phrasing:

> "I'm about to clone with `gt`. The current `gh` user is `<active-user>` —
> should I use that, or a different GitHub user (e.g. a work account)?"

Then pass the choice via `--user <gh-username>`.

Other flags to be aware of:

- HTTPS URLs trigger an interactive SSH prompt by default. In non-interactive
  agent contexts pass `--ssh` (convert to SSH) or `--no-ssh` (keep HTTPS) to
  avoid hanging on the prompt.
- Pass `--no-setup-auth` only if the user has explicitly said not to wire up
  direnv auth.
- After clone, gt may run user-configured setup templates (e.g. dropping in
  CLAUDE.md, cloning agentic-toolkit). It prints the plan and prompts before
  running. In agent contexts pass `--yes` to accept the plan non-interactively,
  `--no-setup` to skip, or `--setup <a,b,c>` to pick specific templates.

Example:

```sh
gt clone --ssh --user pedromvgomes git@github.com:owner/repo.git
cd repo
```

After cloning, the directory layout will be:

```
repo/
  .bare/                 # bare git repo
  .git                   # pointer file
  main/                  # default-branch worktree (or master/<default>)
  feature/  fix/  chore/ # empty typed worktree dirs
```

Treat `repo/main/` (or whatever the default branch is) as the working copy for
default-branch edits. Do NOT `cd` into `.bare/`.

## Step 4 — Creating a worktree

Use `gt wt add <type/name>` — never `git worktree add`:

```sh
gt wt add feature/new-thing            # branches off the default branch
gt wt add fix/bug-123 --from main      # branch off a specific source
gt wt add chore/cleanup
```

`<type>` must be one of the configured worktree types (default: `feature`,
`fix`, `chore`; check `<gt-root>/.gt.yaml` or `~/.config/gt/config.yaml` for
overrides). The new worktree lives at `<gt-root>/<type>/<name>/` and is checked
out on a branch named `<type>/<name>`.

`cd` into the new worktree directory to do the work.

## Step 5 — Listing worktrees

```sh
gt wt list
```

Use this to discover existing worktrees and their HEADs before creating a new
one — it avoids redundant worktrees for the same logical task.

## Step 6 — Removing a worktree

When work is merged or abandoned, clean up with `gt wt rm`:

```sh
gt wt rm new-thing             # remove worktree, keep the local branch
gt wt rm new-thing --branch    # remove worktree AND delete the local branch
```

Pass only the short `<name>` (without the type prefix). Use `--branch`/`-b`
when the branch has been merged or is no longer needed.

For bulk cleanup:

```sh
gt wt nuke                     # remove ALL typed worktrees (keeps branches)
gt wt nuke --branches          # ...and force-delete their branches
gt wt prune-branches --dry-run # preview branch cleanup of orphaned branches
gt wt prune-branches           # delete branches with no active worktree
                               # (preserves main/master/default/scratch)
```

`nuke` is destructive — confirm with the user before running it.

## Step 7 — Re-running setup or re-wiring auth

If the user wants to (re-)apply their configured setup templates to a repo
they already cloned (or to a plain non-gt git repo), use `gt setup`:

```sh
gt setup                          # match by origin URL, prompt, run
gt setup --setup agentic-toolkit  # run a specific template
gt setup --from agentic-toolkit   # resume after a previous failure
gt setup --dry-run                # print plan, do nothing
gt setup --no-setup               # no-op
```

Pass `--yes` in non-interactive contexts. `gt setup` works in any git repo,
not just gt-managed ones — it reads the `origin` remote URL for matching.

To re-wire the `GH_TOKEN` direnv auth or rewrite the `origin` remote to use
the configured SSH host alias for a specific GitHub user:

```sh
gt set-auth --user <gh-username>        # idempotent .envrc + direnv allow
gt set-ssh-remote --user <gh-username>  # rewrite origin to ssh alias
```

## Inspecting the user's setup config

To answer "what templates would run here?" without executing anything:

```sh
gt config show               # print the global config
gt config path               # show where it lives
gt config validate           # parse + validate
gt setup --dry-run           # plan for the current repo
```

Do NOT edit the user's config without an explicit request — templates run
shell with the user's environment, so config edits are equivalent to editing
their shell profile.

## Detection heuristic

You are in a gt-managed repo if any of these are true:

- `<root>/.bare/` exists and `<root>/.git` is a small pointer file.
- `<root>/.gt.yaml` exists.
- The directory tree under `<root>/` contains `main/`, `feature/`, `fix/`, or
  `chore/` worktree subdirectories.

In that case, prefer `gt` commands for any worktree or repo-level operation,
and run file edits inside the relevant worktree directory rather than at the
gt-managed root.

## Things to avoid

- Do NOT run `git clone` for new repos in this environment — use `gt clone`.
- Do NOT run `git worktree add` / `git worktree remove` directly — use
  `gt wt add` / `gt wt rm`. Raw `git worktree` calls bypass the typed layout
  and the configured worktree-types validation.
- Do NOT edit files inside `.bare/`.
- Do NOT delete the default-branch worktree (`main/` or equivalent).
- Do NOT use `gt scratch` — the scratch worktree is reserved for the user's
  manual exploration, not for agent work.
- Do NOT do session work at the gt-managed root or directly on the default
  branch — always `gt wt add` your own worktree first.
