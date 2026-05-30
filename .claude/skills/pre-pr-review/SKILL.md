---
name: pre-pr-review
description: |
  Multi-agent pre-PR code review of the current worktree's changes vs the branch's source. Selects a stack-specific reviewer panel (Kotlin/Spring,
  React, etc.) from `agents.json`, fans the specialists out in parallel, then consolidates findings into a RED/AMBER/GREEN triage table so the
  user can decide what to fix before opening the PR. Trigger on phrases like "review my branch", "pre-PR review", "review before PR",
  "check my changes before I open the PR", or "run a code review on this branch".
---

# Pre-PR multi-agent code review
Review the current worktree's changes against the branch's **source (parent) branch** using a stack-specific panel of specialist subagents, then
triage their findings as RED / AMBER / GREEN so the user can pick what to fix before opening a PR.

In a stacked-branch workflow the source branch is often another feature branch, not `main`. The skill discovers the parent with plain `git` and lets
the user override the detected value before the review starts.

The subagent roster lives in `agents.json` next to this file, keyed by stack. Each stack maps to a list of agents, each declaring `name`, `model`,
`prompt_file` (relative to the skill dir), and a short `description`. To add a stack, retune an axis, or change models, edit `agents.json` or the
relevant prompt file under `prompts/<stack>/` — do **not** edit this SKILL.md.

## Workflow

### Phase 1 — Detect parent, gather diff, pick stack, confirm scope
Do all of the following before launching any subagent.

#### 1.1 Detect and confirm the parent branch
Run the parent-detection script:

    bash ${SKILL_DIR}/scripts/detect-parent.sh

It returns JSON like `{"current":"...","parent":"...","base":"...","candidates":[...]}`.

Tell the user in one short line which parent was detected, then ask them to confirm or override with `AskUserQuestion`:
- **Use detected parent (`<PARENT>`)** — recommended.
- **Use `main`** — only offered if the detected parent isn't already `main`.
- **Specify a different branch** — user types a branch name.

#### 1.2 Gather the diff
Capture three diffs and concatenate them so nothing on the worktree is missed:

```bash
git diff "$BASE"...HEAD   # committed-on-branch (vs parent)
git diff --cached         # staged
git diff                  # unstaged
```

Collect the changed-files list so subagents can `Read` full-file context when a hunk alone is ambiguous:

```bash
git diff --name-status "$BASE"
git diff --cached --name-status
git diff --name-status
```

If the combined diff is empty, stop and tell the user there is nothing to review.

#### 1.3 Pick the stack
Read `agents.json` and look at the keys under `agents` — these are the available stacks (e.g. `kotlin-spring`, `react`).

**Auto-detect a default** by scanning file extensions and paths in the changed-files list:
- `kotlin-spring`: `.kt`, `.kts`, `build.gradle*`, `libs.versions.toml`, paths containing `src/main/kotlin` or `src/test/kotlin`.
- `react`: `.tsx`, `.jsx`, `package.json`, `next.config.*`, `vite.config.*`, paths containing `src/components` with `.ts`/`.tsx`.

For each defined stack, count matching files; the stack with the most matches is the suggested default. If counts tie or nothing matches,
no default is suggested.

Behavior:
- **One stack defined** in `agents.json` → use it silently, just tell the user which one (one line).
- **Multiple stacks, clear default** → ask via `AskUserQuestion` with the detected default first.
- **Multiple stacks, no default** → ask via `AskUserQuestion` with all options, no recommendation.

If the diff appears to span multiple stacks (e.g. both `.kt` and `.tsx` files with non-trivial counts), call this out in the question
preamble — the user may want to run the review twice, once per stack. The skill itself only runs one stack per invocation.

#### 1.4 Confirm scope
Write a short prose paragraph (not a file list) describing the change at a high level — what subsystems it touches, roughly how many
files, structural moves. Example shape:

> *"This branch restructures `common-logging` as an independent module: ~14 files moved into `modules/common-logging/`, plus migrations of two
> integration tests from `common-reactive` and `common-blocking`. Build config updates in `libs.versions.toml` and the
> `AutoConfiguration.imports` registry. No vendored or generated files detected."*

Then call out any files that probably should **not** be reviewed by the subagents. Heuristics for "likely exclude":

- Lock files: `*.lock`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `Cargo.lock`, `gradle.lockfile`.
- Generated code: paths under `build/`, `generated/`, `target/`, `dist/`, `.next/`, `node_modules/`, or files with a "DO NOT EDIT" header.
- Large binary or data files (non-text, or > ~500 KB).
- Snapshot / fixture files: `*.snap`, large `*.json` test fixtures.
- Vendored third-party code: `vendor/`, `third_party/`.
- Diffs that are purely whitespace / formatting / rename-only.

If nothing matches, state "no exclusion candidates detected".

Use `AskUserQuestion`:
- **Proceed with this scope** — review everything in the diff.
- **Proceed but exclude the flagged files** — drop the called-out files (only offer this option if you actually flagged some).
- **Cancel** — stop here.

Do not start Phase 2 until the user picks an option.

### Phase 1.5 — Extract repo conventions (single shared pass)
1. Determine which modules the diff touches (parse the changed-files list and extract distinct module roots), then run:

   bash ${SKILL_DIR}/scripts/find-convention-docs.sh <module-paths...>

   It returns a JSON array of doc paths that exist. If empty, skip skip this phase. Tell the user "no convention docs found — reviewers will
   use general best practices for the <stack> stack." Proceed directly to Phase 2 with an empty conventions summary.

2. Otherwise, launch one `Agent` call:
   - `subagent_type: "general-purpose"`
   - `model: "sonnet"`
   - `description: "Pre-PR review: extract conventions"`
   - `prompt:` the extractor prompt below, with the list of doc paths that exist and the changed-files list appended.

3. The extractor returns a markdown summary structured as a bulleted list of rules, each with a source citation. Expected shape:

   ```markdown
      ## Repo conventions extracted from docs
   
      ### Root-level
      - **Unit tests run via the `unitTest` gradle task, not `test`.**
        Source: `docs/CODE_STANDARDS.md` §Testing.
      - **URL pattern matching uses `PathPatternParser`, not `AntPathMatcher`.**
        Source: `docs/ARCHITECTURE.md` §Routing.
   
      ### Module: `modules/common-logging`
      - **All filters extend `AbstractLoggingFilter`.**
        Source: `modules/common-logging/AGENTS.md`.
   
      ## Conventions not extracted (out of scope for this diff)
      - Build pipeline rules — diff doesn't touch CI config.
   ```

4. If the extractor returns an empty list (docs exist but contained no rules the diff could violate), proceed with an empty summary.

5. Pass the conventions summary into every Phase 2 agent prompt as a new section between the shared preamble and the per-axis prompt body. If
   the summary is empty, omit the section entirely rather than including an empty placeholder.

#### Extractor prompt
> You are extracting repo-specific conventions from documentation files
> so that downstream code reviewers can hold a diff against them. You are
> not reviewing code yourself.
>
> You will receive:
> - A list of doc file paths that exist in this repo (at the root and at module roots).
> - The changed-files list for the diff under review.
>
> Procedure:
>
> 1. `Read` each listed doc file.
> 2. Extract every rule that is (a) prescriptive (uses words like "must," "always," "never," "do not," or is structured as a hard rule rather
>    than a recommendation) AND (b) could plausibly be violated by code in the changed-files list. Skip rules about parts of the codebase
>    not touched by the diff. Skip aspirational language, historical context, and pure-style preferences (indentation, line length, import ordering).
> 3. For each rule, record:
>    - The rule itself, in one sentence, in the doc's own terms.
>    - The source: file path plus section heading or line range.
>    - The scope: repo-wide, or specific module path.
> 4. Output the markdown summary in the structure the orchestrator specified. Group rules by scope (root-level first, then by module).
>    Add a brief "Conventions not extracted" section listing major rule categories you skipped and why, so the orchestrator can confirm
>    nothing relevant was missed.
>
> If a doc contains no rules that the diff could violate, return an empty list under that scope. Returning an entirely empty summary is a
> valid outcome.
>
> Do not infer conventions that aren't written in the docs. Do not include rules whose source you can't cite. Do not paraphrase rules in
> ways that change their meaning — quote the doc's wording where it matters.

### Phase 2 — Fan out the specialist subagents in parallel
1. From the parsed `agents.json`, select `agents[<chosen_stack>]`. For each entry, read the markdown body at `prompt_file` (path relative to the 
   skill dir).

2. Announce what will run in one line, including the agent descriptions from `agents.json` — gives the user a quick sense of what's being checked.
   Example:
   > *"Launching 3 reviewers on the `kotlin-spring` stack: performance, security, best-practices."*

3. For every agent in the selected roster, launch one `Agent` call with:
   - `subagent_type: "general-purpose"`
   - `model:` the value from the roster (`"opus"` / `"sonnet"` / `"haiku"`)
   - `description:` `"Pre-PR review: <agent.name>"`
   - `prompt:` the assembled prompt described below.

   **All `Agent` calls MUST be issued in a single message** so they execute in parallel. Do not chain them sequentially.

4. The prompt passed to each subagent is the concatenation of:

   ```
      <shared preamble — copy verbatim from the section below>
   
      ---
   
      ## Repo conventions extracted from docs
   
      <the conventions summary returned by the Phase 1.5 extractor agent>
   
      (Omit this section entirely if the extractor returned an empty summary or Phase 1.5 was skipped.)
   
      ---
   
      <contents of the agent's prompt_file>
   
      ---
   
      ## Changed files
   
      <output of the three `git diff --name-status` commands>
   
      ## Unified diff
   
      <the combined diff payload from Phase 1, with any files the user chose to exclude removed>
   ```
   
   #### Shared preamble (use verbatim in every subagent prompt)
   
   > You are one of several specialist reviewers running in parallel as part of a multi-agent code review panel. Other reviewers cover the axes you are
   > told to ignore — do not duplicate their work. If an issue spans multiple axes, file only your axis and note the others in one line so the
   > coordinator can dedupe.
   >
   > If a "Repo conventions extracted from docs" section appears below, treat it as the authoritative source for repo-specific rules. Hold the diff
   > against those rules and cite them by their source when filing convention findings. If the section is absent, no convention docs were found — rely
   > only on your axis's scope, do not invent repo conventions.
   >
   > Return findings in this exact YAML-ish schema, one entry per finding, nothing else outside the list:
   >
   > ```yaml
   > - severity: RED | AMBER | GREEN
   >   category: <your axis, e.g. "security:injection" or "perf:n+1">
   >   file: path/to/file.kt
   >   line: <line or range, or "n/a" if cross-cutting>
   >   issue: <one-sentence description of the problem>
   >   evidence: <short quote or reference from the diff/file; for convention
   >             findings, also quote the rule and its source from the
   >             conventions summary>
   >   proposed_action: <concrete fix, not a vague suggestion>
   >   confidence: high | medium | low
   > ```
   >
   > Severity calibration (applies identically across all reviewers and overrides any conflicting bar in your axis prompt):
   > - **RED** — must fix before merge: real bug, exploitable vuln, data loss, significant perf regression on a hot path, breaks a documented contract.
   >   - **AMBER** — should fix: latent risk, maintainability problem, minor perf issue, convention violation with real downstream cost.
   >   - **GREEN** — nice to have: nit, style, opportunistic improvement.
   >
   > Every finding must quote the offending line(s) in `evidence`. If you can't point to specific code, don't file it. Returning an empty list is a
   > valid outcome — false positives are more costly than misses.

5. **Show the user the extracted conventions before fan-out.** Render the summary as-is (it's already markdown) under a short framing line:

   > *"Here are the conventions reviewers will be held against. Anything
   > missing, wrong, or in scope for this diff that I should drop before
   > launching the panel?"*
   
   Use `AskUserQuestion` with three options:
     - **Use as-is** — proceed to Phase 2 with this summary.
     - **Edit the summary** — user types corrections; apply them inline and show the revised summary, then re-ask.
     - **Skip conventions entirely** — proceed with an empty summary (reviewers will rely on Scope alone).
   
   This gate is especially valuable on the first run against a new repo, when extraction quality is unverified. On repeated runs against the
   same repo, the user can move past it quickly.

### Phase 3 — Consolidate

When all subagents have returned:

1. Parse every agent's finding list.
2. **Deduplicate** — when two agents flag the same `file` + `line` for the same underlying issue, keep the higher severity and merge their 
   `proposed_action` text. Preserve both `category` values (comma-joined) so the user sees which axes converged.
3. **Sort** by severity (RED → AMBER → GREEN), then within a severity by file path so related findings cluster.
4. **Drop noise** — silently discard any GREEN finding with `confidence: low`.
5. Number the surviving findings continuously across severities (1, 2, 3 …) so the user can refer to them in a follow-up turn.

### Phase 4 — Present the triage

Render three sections, one per severity, each a markdown table. Omit a section entirely if it would be empty.

```markdown
## RED — must fix before PR
| # | Category           | Location             | Issue                                       | Proposed action                            |
|---|--------------------|----------------------|---------------------------------------------|--------------------------------------------|
| 1 | security:injection | UserController.kt:42 | Unparameterized SQL built from request body | Switch to JdbcTemplate parameterized query |

## AMBER — should fix
| # | Category | Location | Issue | Proposed action |
| ... |

## GREEN — nice to have
| # | Category | Location | Issue | Proposed action |
| ... |
```

After the tables, ask the user:

> *"Tell me which findings to fix (e.g. 'all RED', 'fix 1, 4, 7', 'skip all'). I won't modify code without your explicit selection."*

### Phase 5 — Apply selected fixes (only if the user picks any)

Implement just the selected findings using the same diligence as any other edit task. After editing, run the project's standard checks
for the chosen stack (e.g. `unitTest` for Kotlin/Spring in this monorepo, `npm test` / `pnpm test` / `vitest` for React — match the
project's existing convention). Report what was changed and which findings remain unaddre