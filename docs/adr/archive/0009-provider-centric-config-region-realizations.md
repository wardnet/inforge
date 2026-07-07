---
status: superseded by ADR-0011
date: 2026-06-05
issue: "#58"
---

# Provider config owns per-region realizations; abstraction tables stay cloud-agnostic

> **Superseded by [ADR-0011](../0011-regions-yaml-region-and-provider-authority.md).** This ADR put
> provider config (credentials + realizations) under `providers.<name>` in `variables.yaml`, keeping
> `regions.yaml` as a cloud-agnostic abstract-region → slug table. ADR-0011 moves all provider config
> *into* `regions.yaml`, keyed per region, making it the single authority for which regions deploy and
> how each provider realizes them. The "by provider, fully-explicit, no inheritance, enforce at the
> provider boundary" decisions below still hold — only the *file* that owns the per-provider block
> changed (from `variables.yaml` to each region's entry in `regions.yaml`).

inforge has cloud-agnostic abstractions — regions, sizes, images — that each provider must translate
into concrete values (for Hetzner: a datacenter `location` + `network_zone`, a server type per size,
an image id per canonical image) alongside its credentials. These translations had drifted across
three places under an overloaded `providers.<name>` key: region topology lived in the region table
(`regions.yaml`), credentials in `variables.yaml`, and the size→server-type and image maps were
hardcoded in provider code. Worse, the same `providers.<name>` key accepted *different* fields
depending on which file it appeared in, so a field written in the wrong file was silently ignored.

We decided to make provider configuration **provider-centric**: everything a provider needs lives in
one block per provider in `variables.yaml` under `providers.<name>` — credentials plus a region-keyed
map of **region realizations**. A region realization is the *complete* concretization of one abstract
region on that provider: `location`, `network_zone`, `serverTypes` (size name → SKU), and `images`
(canonical image → provider image id). The layer-1 vocabulary tables stay cloud-agnostic and
provider-free: `regions.yaml` maps an abstract region to its slug, and `sizes.yaml` enumerates the
valid size names. Resource→provider selection is unchanged — each resource still names its provider
via the required `provider:` field.

## Considered options

- **By abstraction** — each vocabulary table carries its own per-provider sub-map (the region entry
  holds `{hetzner: {location}}`, the size entry holds `{hetzner: cx23}`). This is the direction the
  code had drifted. Rejected: it scatters one provider's config across every table, leaks cloud
  specifics into the cloud-agnostic vocabulary, and means adding a provider touches every table.
- **By provider** (chosen) — one block per provider owns credentials and all translations. Gives a
  single home per provider, keeps the vocabulary tables describing inforge's own language, and makes
  adding a provider a single-block edit.

## Consequences

- **Realizations are fully explicit per region — no global defaults, no inheritance**, consistent
  with ADR-0002's replace-wholesale rationale (a reader sees the complete truth in one place). This is
  not cosmetic: Hetzner server-type availability genuinely varies by location (e.g. `cax*` ARM types
  are not offered in `ash`), so `serverTypes` *must* be expressible per region. `images` follows the
  same rule for one consistent "a realization is the complete truth" model; repetition across regions
  is handled with YAML anchors, not an inforge inheritance rule.
- **Amends ADR-0003.** The size table no longer carries `cpus`/`memory` — no provider consumed them
  (Hetzner selects purely on server type). A size is now just a validated name; each provider maps the
  name to a concrete SKU via its realization's `serverTypes`.
- A region that a provider's resource deploys into must have a complete realization in that provider's
  block. Enforcement is at the **provider boundary**, not in `internal/validate` (which stays
  cloud-agnostic and does not import the provider packages): `ResolveRegion` rejects a missing region
  or a realization without `location`/`network_zone`, and `Create` rejects a `size`/`image` absent
  from the realization's `serverTypes`/`images`. These checks run during `pulumi preview`/`up` and
  fail closed with a clear, actionable message rather than surfacing as an opaque hcloud error.
