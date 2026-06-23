# A change to a resource's authored schema or user-facing behaviour must update the public docs under website/docs

The repo has **two** documentation audiences, and they are easy to update asymmetrically:

- **Agent-facing**: `AGENTS.md`, `internal/CONTEXT.md`, and `.agents/rules/*.md` — how the code works for
  contributors and coding agents.
- **User-facing**: `website/docs/` (the Docusaurus site) — how an operator **authors** resources. The
  per-resource reference pages live in `website/docs/resources/` (`ingress.md`, `service.md`,
  `compute.md`, `network.md`, `database.md`, `dns.md`, `secrets.md`), and CLI/concept pages live under
  `website/docs/cli/` and `website/docs/concepts/`.

When a change touches **what an operator writes in a manifest** or **how that manifest behaves**, the
matching `website/docs/` page must change in the **same** PR. Concretely, this is required when you:

- add, remove, or rename a YAML field on a spec in `internal/types` (and its `schemas/*.json`) — update
  the page's **schema example block** and its **field table**;
- change a validation rule that constrains what is authorable (a new required field, a new collision
  rule, a relaxed/!tightened constraint) — update the prose so the documented rules match what
  `inforge validate` enforces;
- change a default (e.g. a port that defaults to `81`) — state the new default on the page;
- change runtime behaviour an operator can observe (a new listener, a new derived DNS record, a changed
  proxy path) — update the behaviour section.

## Applies to

Any PR that edits `internal/types/*.go` spec structs, `schemas/*.json`, or the authoring-facing rules in
`internal/validate`. Map the changed resource to its page: `IngressSpec`/`schemas/ingress.json` →
`website/docs/resources/ingress.md`; `ServiceSpec`/`RouteSpec`/`schemas/service.json` →
`website/docs/resources/service.md`; and so on. A new field surfaced on two specs (e.g.
`health_probes_port` on both ingress and service) must be documented on **both** pages, cross-linked.
The agent-facing docs (`AGENTS.md`, `.agents/rules/`, `internal/CONTEXT.md`) are **not** a substitute —
they serve a different reader.

## Why

The website is the only documentation an operator reads before writing a manifest. If a field or rule
ships in the schema and validator but not on the resource page, operators either can't discover it or
hit `inforge validate` errors the docs don't explain — and the gap is invisible from the code review,
since the build and tests pass without the doc. Treating the user-facing page as part of the change set
(not a follow-up) keeps the authored contract and its documentation from drifting.
