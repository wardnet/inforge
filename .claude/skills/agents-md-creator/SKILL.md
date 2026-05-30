---
name: agents-md-creator
description: |
  Use this skill whenever you need to create or update an AGENTS.md file for a repository or module.
  This includes when a user asks to set up AI agent instructions, configure coding agent behavior,
  or create documentation that guides AI assistants working in a codebase.
  Make sure to use this skill whenever the task involves AGENTS.md, copilot-instructions, or similar
  agent configuration files. This skill also ensures a sibling CLAUDE.md exists alongside the
  AGENTS.md, containing the Claude Code import directive `@AGENTS.md`, so that Claude Code picks
  up the same canonical content as every other agent.
---

# AGENTS.md Creator

Create effective AGENTS.md files that give AI coding agents the context they need to work well in a codebase.

## Overview

An AGENTS.md file is the primary way to instruct AI coding agents about a project's conventions, structure,
boundaries, and workflows. A well-written AGENTS.md dramatically improves agent output quality by reducing
guesswork and enforcing project-specific standards.

AGENTS.md is the canonical, platform-neutral source. To wire Claude Code into the same content, a sibling
`CLAUDE.md` file is created next to every AGENTS.md, containing only the Claude Code import directive
`@AGENTS.md`. This means there is one source of truth — AGENTS.md — and Claude Code, Cursor, Copilot, and
every other agent pointed at AGENTS.md (directly or via the import) read the same thing.

## Workflow

1. **Analyze the codebase** — Before writing anything, explore the repository to understand its structure,
   tech stack, conventions, and existing documentation (README, CONTRIBUTING, etc.)
2. **Determine scope** — Is this for a repo root or a specific module? Root-level files cover broad guidance;
   module-level files cover module-specific details.
3. **Draft the AGENTS.md** — Follow the structure and guidelines in the references.
4. **Ensure the sibling CLAUDE.md** — Check whether `CLAUDE.md` exists.
   - If it does NOT exist: create it with exactly one line of content: `@AGENTS.md` (followed by a single trailing newline). Nothing else.
   - If it DOES exist and its content is already exactly `@AGENTS.md` (with optional trailing whitespace/newline): leave it alone.
   - If it DOES exist with different content: **do not overwrite**. Note this in your output to the user — the user may have customized CLAUDE.md
     intentionally and should decide how to reconcile.
5. **Include the Rules Pointer (when applicable)** — If `.agents/rules/` already exists with files, OR if rules are being added in the same 
   operation, the AGENTS.md must contain the canonical Rules Pointer section. See the "Rules Pointer" subsection under File Organization for the exact text.
   When creating a brand-new AGENTS.md with no rules yet present or planned, omit this section.
6. **Review with the user** — Present the draft AGENTS.md (and note CLAUDE.md + rules-pointer status) and iterate based on feedback.

## Key Principles

- **Be specific, not generic** — Say "Kotlin 1.9 with Gradle Kotlin DSL and Spring Boot 3" not "JVM project"
- **Show, don't tell** — One real code snippet beats three paragraphs of description
- **Commands first** — Place executable commands (build, test, lint) near the top for easy reference
- **Progressive disclosure** — Keep the main AGENTS.md concise; link to detailed reference files for deep dives
- **Set clear boundaries** — Explicitly state what agents should and should not do
- **One source of truth** — AGENTS.md is canonical. CLAUDE.md is an alias via `@AGENTS.md`. Do not duplicate content into CLAUDE.md.

## Structure Guide

Use the following sections. Not all are required — include only what's relevant to the project.

Refer to [references/structure.md](references/structure.md) for the detailed structure template and examples.

## File Organization

### AGENTS.md Placement
- **Root AGENTS.md** (or `.github/AGENTS.md`) — Repo-wide guidance: tech stack, build commands, code style, git workflow
- **Module/directory AGENTS.md** — Module-specific patterns, dependencies, and conventions that override or extend root guidance

### CLAUDE.md Sibling

Every AGENTS.md must have a sibling `CLAUDE.md` in the same directory, containing exactly:

```
@AGENTS.md
```

This is Claude Code's file-import directive: when Claude Code loads CLAUDE.md, the `@AGENTS.md` line is replaced with the contents of the sibling AGENTS.md.
The result is that Claude Code reads the same content as every other agent, with no duplication and no drift.

Rules:
- CLAUDE.md contains only `@AGENTS.md` (plus a trailing newline). No other content. No headings, no comments.
- One CLAUDE.md per AGENTS.md — root and every module-level AGENTS.md each get their own sibling CLAUDE.md.
- Never write project guidance directly into CLAUDE.md. If a Claude-specific override is genuinely needed, raise it with the user — usually the right
  answer is to put the content in AGENTS.md (so all agents benefit) or in a rule file under `.agents/rules/`.

### Rules Pointer (canonical source)

When an AGENTS.md is associated with a `.agents/rules/` directory (existing now, or being added in the same operation), it must include a `## Rules` section
at the top, after any title/intro, with **exactly this text** — this is the canonical version that other tools (like the `wrap-session-reviewer` agent) read from this skill to stay in sync:

<!-- BEGIN CANONICAL RULES POINTER -->
```markdown
## Rules

This module has prescriptive rules in `.agents/rules/`. **Read every file in that directory before making changes here, and follow each rule strictly.**
Each file contains one rule. New rules go in that directory — one file per rule, kebab-case filename matching the rule's intent.
```
<!-- END CANONICAL RULES POINTER -->

Rules:
- This is the single source of truth for the pointer text. Any tool that needs to add this section to a new or existing AGENTS.md must read this skill file
  and extract the contents between the `BEGIN CANONICAL RULES POINTER` and `END CANONICAL RULES POINTER` HTML comments.
- Include the pointer when `.agents/rules/` exists or is being created. Omit it for AGENTS.md without any associated rules.
- If updating an AGENTS.md that already has a `## Rules` section, leave it alone (do not duplicate, do not rewrite). The pointer is meant to be set
  once and forgotten.
- Place the section near the top, after the title and any brief intro, but before deeper sections like build commands or architecture notes.


When the AGENTS.md needs to reference detailed documentation (architecture deep dives, API patterns, schema definitions, etc.),
place those files in an `.agents/references/` directory at the same level as the AGENTS.md file.

```
# Root-level example
├── AGENTS.md
├── CLAUDE.md                  # contains only: @AGENTS.md
├── .agents/
│   ├── references/
│   │   ├── architecture.md
│   │   ├── code-style.md
│   │   └── api-patterns.md
│   └── rules/
│       └── <rule-files>.md

# Module-level example
├── modules/my-module/
│   ├── AGENTS.md
│   ├── CLAUDE.md              # contains only: @AGENTS.md
│   └── .agents/
│       ├── references/
│       │   ├── module-patterns.md
│       │   └── data-model.md
│       └── rules/
│           └── <rule-files>.md
```

The AGENTS.md should link to these reference files with clear guidance on when to read them.
Keep the main AGENTS.md under ~200 lines and offload details into references.

## Boundary Tiers

Define agent permissions using three tiers:

- **Always do** — Actions the agent should take without asking (e.g., run linter, follow naming conventions)
- **Ask first** — Actions that need user confirmation (e.g., delete files, modify CI config, change public APIs)
- **Never do** — Hard limits (e.g., commit secrets, skip tests, modify vendor code)

## Anti-Patterns

- Walls of text with no structure or headings
- Generic advice that applies to any project ("write clean code")
- Contradicting existing README or CONTRIBUTING docs
- Overly restrictive rules that prevent agents from being useful
- Duplicating information already in other project docs — link to them instead
- Writing content into CLAUDE.md instead of AGENTS.md (creates drift between Claude Code and other agents)
- Multiple AGENTS.md files at the same scope, or an AGENTS.md without a sibling CLAUDE.md
