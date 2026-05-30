---
name: wrap-session
description: |
  Use at the end of a working session to review and update documentation (AGENTS.md and per-rule files under .agents/rules/) for any modules
  touched during the session. Asks the user which change scope to review (uncommitted, branch commits, or both), then dispatches to the wrap-session-reviewer
  subagent to do the actual work on Sonnet. Triggers on user phrases like "wrap up", "wrap this session", "end of session", "session review", "review docs",
  "let's close out", "we're done with this session", or any explicit signal that the user is finishing a session and wants documentation considered for the
  work that was done.
---

# Wrap Session

Run at the end of a working session to update `AGENTS.md` and per-rule files under `.agents/rules/` for any modules touched during the session.

This skill is **orchestration only**. It asks one question, then hands off to the `wrap-session-reviewer` subagent which runs on Sonnet.
Keep token usage here minimal — do not do the review work yourself.

## When to use

The user explicitly signals end of session. Do NOT auto-trigger mid-session.

## Workflow

### Step 1 — Sanity check

Run `git rev-parse --show-toplevel`. If it fails, abort with: "This skill requires a git repo. Aborting." Capture the output as `<repo_root>`.

### Step 2 — Ask the user which change scope to review

Ask exactly this, verbatim:

> Which changes should I review for documentation updates?
>
> **1. Uncommitted** — staged, unstaged, and untracked files (everything in `git status`)
> **2. Branch commits** — commits on the current branch not yet on the default branch
> **3. Both** — the union of the above
>
> Pick 1, 2, or 3.

Wait for the user's answer. Do not proceed without one of the three. If the user replies with anything ambiguous, ask again with the same three options.

### Step 3 — Dispatch to the subagent

Invoke the `wrap-session-reviewer` subagent via the Task tool. The prompt to the subagent must include:

- `scope`: one of `uncommitted`, `branch_commits`, or `both` (derived from the user's pick)
- `repo_root`: the absolute path from step 1
- `session_summary`: a short (1–3 sentence) prose summary of what this session worked on, drawn from the recent conversation.
  This gives the reviewer context the diff alone won't carry. If the session is very long, summarize at the level of "themes and modules touched",
  not specifics.

Example invocation payload (adapt to the Task tool's actual format):

```
Run a wrap-session documentation review.

scope: branch_commits
repo_root: /home/pedro/code/example-monorepo
session_summary: Refactored the auth module to use the new SMART-on-FHIR launch flow,
and added a Temporal workflow for token refresh. Also touched the shared http client
in lib/common to support custom retry policies.
```

### Step 4 — Present the subagent's report

When the subagent returns, show its summary to the user **as-is**. Do not editorialize, do not re-summarize. The user can inspect actual writes
via `git diff` and revert anything they don't want.

If the subagent reports no changes to make (empty change set, no docs warranted), pass that through too.

## Constraints

- Do NOT do the review yourself. Always dispatch to the subagent.
- Do NOT skip the scope question — it's the whole point of this skill being interactive.
- Do NOT modify any files directly from this skill.
