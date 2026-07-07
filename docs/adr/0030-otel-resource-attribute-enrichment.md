---
status: accepted
date: 2026-06-28
issue: "#148"
---

# OTel resource-attribute enrichment (cloud.*/host.*)

Extends the #134 observability env-var contract so every service's telemetry
(metrics, traces, logs shipped to Grafana Cloud) is tagged with the cloud/host
resource attributes inforge alone knows at deploy time. Today the only host-level
attribute is `host.id` (`INFORGE_HOST_ID`); the rest of the deploy's ground truth
(provider, region, datacenter, machine size) is resolved inside the Hetzner
provider and discarded.

We inject **four** new resource attributes — and only four: the ones where inforge
is the *sole* authority, i.e. facts the running process provably cannot determine
itself.

| OTel attribute | Value | Source |
|---|---|---|
| `cloud.provider` | `hetzner` | provider self-names |
| `cloud.region` | Hetzner `network_zone` (e.g. `us-east`) | provider |
| `cloud.availability_zone` | Hetzner `location` (e.g. `ash`) | provider |
| `host.type` | server-type SKU (e.g. `cx23`) | provider |

## Considered options

- **`host.name` and `os.type` — rejected.** `host.name` (the OS hostname) is
  readable by the process via `gethostname()` and would duplicate the already-injected
  `host.id` (the unique cloud resource name). `os.type` is always `linux` and is
  trivially self-detectable (the OTel SDK ships an OS resource detector). Neither is
  inforge's unique knowledge, so injecting them adds contract surface for no value.
- **`cloud.region` = Hetzner `network_zone`, not the inforge abstract region.** The
  abstract region (`us-east-1`) is already in `INFORGE_DEPLOYMENT_REGION` and mapping
  *that* would be zero new work, but it is inforge's portable abstraction, not the
  provider's region — and it would pair a `cloud.region` from one naming system with a
  `cloud.availability_zone` from another. OTel semconv defines `cloud.region` as the
  *provider's* region; the provider-native hierarchy `network_zone ⊃ location` maps
  exactly onto `cloud.region ⊃ cloud.availability_zone`. We pay one extra provider-sourced
  string to keep both `cloud.*` geo attributes consistent and semantically correct.

## How

Provider-supplied facts reach the descriptor through `types.ComputeOutputs` — the
existing provider→program boundary object, already keyed per-host in `computeOut`.
It gains four plain-`string` fields (`CloudProvider`, `CloudRegion`,
`AvailabilityZone`, `MachineType`), populated by `hetzner.Create()` (plan-time
constants, no Pulumi apply). `renderDescriptor` reads them off the host's
`ComputeOutputs` and writes them into `bootstrapper.Deployment`; `buildEnv` emits
`INFORGE_CLOUD_PROVIDER`, `INFORGE_CLOUD_REGION`, `INFORGE_CLOUD_AVAILABILITY_ZONE`,
and `INFORGE_HOST_TYPE` (omitting any that are empty, so a future non-Hetzner provider
that doesn't supply them emits nothing). This keeps `renderDescriptor` provider-agnostic
and makes `cloud.provider` self-named rather than a hardcoded constant; it also makes
**global services correct for free**, since a global host carries its real placement in
its own `ComputeOutputs` rather than a recomputed abstraction.

The four new env-var names are already protected by the reserved `INFORGE_*` prefix,
so no validation change is needed.

## Consequences

- **Descriptor version bumps 4 → 5.** The bootstrapper decodes descriptors strictly
  (`KnownFields(true)`), so any field addition is a breaking change for an older
  bootstrapper — a major bump is forced, not a judgment call. It is safe because the
  pinned `inforge-bootstrap` binary and the descriptor are written by the same deploy
  (identical lockstep to the #134 v3→v4 bump).
- **Cross-repo, decoupled by the env-var contract.** The consumer side is a four-row
  addition to the `(attribute, env_var)` table in wardnet-cloud
  `crates/common/src/telemetry.rs::resource()`; each row is best-effort (a missing/empty
  var is omitted), so inforge can ship and deploy first and the attributes start
  populating once wardnet-cloud picks them up. The collector of ADR-0031 reuses this
  exact attribute set so host metrics correlate with app telemetry on `host.id`.
