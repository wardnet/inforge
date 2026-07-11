---
sidebar_position: 5
---

# inforge releases

Manage a service's release artifacts. A released build is an **immutable, SHA-and-architecture-keyed
tarball** stored in an R2 release store (`<service>/<SHA>-<arch>.tar.gz`, `arch` ∈ `amd64`/`arm64`),
and the SHA (and detected arch) deployed to each host is recorded in a per-environment **manifest**
(`<service>/manifest.<env>.yaml`). See
[ADR-0016](https://github.com/wardnet/inforge/blob/main/docs/adr/0016-r2-release-artifact-store.md)
and [provision vs deploy](/concepts/provision-vs-deploy).

Releasing is two steps: **push** the artifact to the store, then **deploy** a chosen SHA to the
host(s). The [service release starter](/github-actions/overview#service-release-optional) runs both
from your service repo's CI. A service that ships both architectures pushes **twice** for the same
SHA — once per `--arch` — before `deploy` can succeed against a mixed-arch fleet; `deploy` detects
each target host's real architecture over SSH and delivers the matching binary, failing loudly if a
host's arch was never pushed.

## Subcommands

| Command | Purpose |
|---------|---------|
| `inforge releases push` | Package the service's artifact directory and upload it as `<service>/<SHA>-<arch>.tar.gz`, then prune old artifacts. |
| `inforge releases deploy` | Probe each target host's architecture, download the matching stored SHA, SSH-deliver it to the host(s), restart the unit, and record it in `manifest.<env>.yaml`. |
| `inforge releases list` | Print the SHA, architecture, and timestamp deployed to each host for a service+environment, from the manifest. |

## Configuration

The release store is configured in the infra project's `inforge.yaml`. Its bucket **must differ**
from the Pulumi state bucket (enforced at load):

```yaml title="inforge.yaml"
name: wardnet-infrastructure
backend:
  type: r2
  url: r2://wardnet-state         # Pulumi state
artifacts:
  backend:
    type: r2
    url: r2://wardnet-artifacts    # release store — a DIFFERENT bucket
  keep: 10                         # unpinned (rollback) artifacts kept per service; 0/unset = no pruning
```

R2 access uses `CLOUDFLARE_ACCOUNT_ID` and the `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`
credentials (the same ones the state backend uses).

## `inforge releases push`

```
inforge releases push <env> --service <name> --arch <amd64|arm64> [--sha <sha>] [--deploy-dir ./deployments]
```

Packages the env's `artifact_path` (from `deployments/<service>.yaml`) into a tarball and uploads it
to `<service>/<SHA>-<arch>.tar.gz`. Re-pushing the same (SHA, arch) overwrites it (idempotent). A
service that ships both architectures for one SHA runs `push` **twice**, once per `--arch` — each
build's own CI matrix leg has its own artifact directory, so there is no single invocation that
uploads both. After each upload it **prunes**: artifacts beyond `keep` are deleted oldest-first (all
arch variants of a pruned SHA are deleted together, never leaving one orphaned), but a SHA
referenced by **any** environment's manifest is *pinned* and never deleted (pinned SHAs don't count
toward `keep`). Prune failures are warnings — the upload has already succeeded.

`--arch` is required and must be `amd64` or `arm64`. `--sha` defaults to `$GITHUB_SHA`.

## `inforge releases deploy`

```
inforge releases deploy <env> --service <name> --sha <sha> [flags]
```

Resolves every host the service targets in that environment, then **probes each host's real CPU
architecture over SSH** (`uname -m`) before touching the store. For every distinct architecture
actually needed it verifies `<service>/<SHA>-<arch>.tar.gz` exists — if any host's detected
architecture has no matching pushed artifact, the whole run fails **before delivering to any host**,
naming the host(s), the detected architecture, and the exact R2 key it looked for (with a hint to
`releases push --arch <arch>`). There is no fallback to an unsuffixed key. Once every needed arch is
confirmed present, it downloads each **exactly once** (not once per host), delivers the matching
binary to each host, restarts the inforge-managed unit, and records
`host → {sha, arch, deployedAt}` in `manifest.<env>.yaml` via an `If-Match` compare-and-swap (safe
under concurrent deploys).

For an `mtls_files: true` service it first mints the service's own leaf and SSH-pushes it as
`leaf.age` (needs `INFORGE_SECRETS_KEY`), so the restarted unit never boots against an empty
`/<svc>/mtls`. Every
other service skips this — a plain mesh member's leaf lives with its host's mesh proxy, delivered by
`inforge deploy` / `inforge pki renew`, independent of releases. See
[`inforge pki`](./pki#the-release-time-mint-mtls_files-services-only).

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--service` | `-s` | required | Service name — must match `deployments/<service>.yaml` |
| `--sha` | | `$GITHUB_SHA` | Artifact SHA to deploy (required) |
| `--deploy-dir` | | `./deployments` | Path to the deployments directory |
| `--stack-config` | | `inforge.<env>.yaml` | Path to the infra stack config file |
| `--ssh-key` | | `$INFORGE_DEPLOY_KEY` | Path to the SSH deploy key |
| `--dry-run` | | `false` | Probe host archs and verify every needed artifact is pushed, without delivering (this still opens an SSH connection per host to probe architecture) |

## `inforge releases list`

```
inforge releases list <env> --service <name>
```

Reads `manifest.<env>.yaml` and prints each host's deployed SHA, detected architecture, and timestamp:

```
HOST                                           SHA           ARCH    DEPLOYED
bridge-01.vm.prd.use1.wardnet.network          abc1234def56  amd64   2026-06-09T12:00:00Z
```

`ARCH` shows `-` for an app deployment (architecture-agnostic) or a manifest entry recorded before
arch-awareness existed.

## Examples

```sh
# Build artifact for qa (single-arch fleet), then deploy it
inforge releases push   qa --service api --arch amd64 --sha "$GITHUB_SHA"
inforge releases deploy qa --service api --sha "$GITHUB_SHA"

# A mixed-arch fleet: push both variants for the same SHA before deploying
inforge releases push qa --service api --arch amd64 --sha "$GITHUB_SHA"
inforge releases push qa --service api --arch arm64 --sha "$GITHUB_SHA"
inforge releases deploy qa --service api --sha "$GITHUB_SHA"

# What's live in prd?
inforge releases list prd --service api
```

:::note CI workflow contract
If your CI dispatches into a shared "deploy this service" workflow (rather than running
`inforge releases push`/`deploy` directly), that workflow's inputs must be updated to accept one
artifact URL **per architecture** it builds — one `releases push --arch <arch>` invocation per
matrix leg — before a SHA built for a new architecture can be deployed. A workflow still passing a
single artifact URL with no arch will only ever push one architecture's variant.
:::

## Deployments directory

`releases` reads two files from the deployments directory of the **service** repo:

```yaml title="deployments/inforge.yaml"
platform: wardnet/infra   # platform repo running inforge for your infrastructure
services:
  - api
```

```yaml title="deployments/api.yaml"
environments:
  qa:
    artifact_path: dist
  prd:
    artifact_path: dist
    health_check: /healthz   # optional: HTTP path checked after unit restart
```

## SSH key

The deploy key (used by `deploy`) is resolved in order: `--ssh-key` flag, then `INFORGE_DEPLOY_KEY`.
The key must correspond to `ssh.deployPublicKey` set in `variables.yaml` of the platform repo.

## Related

- [Service release starter](/github-actions/overview#service-release-optional) — the GHA workflow that runs push + deploy
