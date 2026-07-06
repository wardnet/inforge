---
sidebar_position: 5
---

# inforge releases

Manage a service's release artifacts. A released build is an **immutable, SHA-keyed tarball**
stored in an R2 release store (`<service>/<SHA>.tar.gz`), and the SHA deployed to each host is
recorded in a per-environment **manifest** (`<service>/manifest.<env>.yaml`). See
[ADR-0016](https://github.com/wardnet/inforge/blob/main/docs/adr/0016-r2-release-artifact-store.md)
and [provision vs deploy](/concepts/provision-vs-deploy).

Releasing is two steps: **push** the artifact to the store, then **deploy** a chosen SHA to the
host(s). The [service release starter](/github-actions/overview#service-release-optional) runs both
from your service repo's CI.

## Subcommands

| Command | Purpose |
|---------|---------|
| `inforge releases push` | Package the service's artifact directory and upload it as `<service>/<SHA>.tar.gz`, then prune old artifacts. |
| `inforge releases deploy` | Download a stored SHA, SSH-deliver it to the host(s), restart the unit, and record it in `manifest.<env>.yaml`. |
| `inforge releases list` | Print the SHA deployed to each host for a service+environment, from the manifest. |

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
inforge releases push <env> --service <name> [--sha <sha>] [--deploy-dir ./deployments]
```

Packages the env's `artifact_path` (from `deployments/<service>.yaml`) into a tarball and uploads it
to `<service>/<SHA>.tar.gz`. Re-pushing the same SHA overwrites it (idempotent). After uploading it
**prunes**: artifacts beyond `keep` are deleted oldest-first, but a SHA referenced by **any**
environment's manifest is *pinned* and never deleted (pinned SHAs don't count toward `keep`). Prune
failures are warnings — the upload has already succeeded.

`--sha` defaults to `$GITHUB_SHA`.

## `inforge releases deploy`

```
inforge releases deploy <env> --service <name> --sha <sha> [flags]
```

Verifies `<service>/<SHA>.tar.gz` exists (fails with a clear message if you forgot to `push`),
downloads it, delivers it to every host the service resolves to in that environment, restarts the
inforge-managed unit, and records `host → {sha, deployedAt}` in `manifest.<env>.yaml` via an
`If-Match` compare-and-swap (safe under concurrent deploys).

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
| `--dry-run` | | `false` | Resolve targets and verify the artifact without delivering |

## `inforge releases list`

```
inforge releases list <env> --service <name>
```

Reads `manifest.<env>.yaml` and prints each host's deployed SHA and timestamp:

```
HOST                                           SHA           DEPLOYED
bridge-01.vm.prd.use1.wardnet.network          abc1234def56  2026-06-09T12:00:00Z
```

## Examples

```sh
# Build artifact for qa, then deploy it
inforge releases push   qa --service api --sha "$GITHUB_SHA"
inforge releases deploy qa --service api --sha "$GITHUB_SHA"

# What's live in prd?
inforge releases list prd --service api
```

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
