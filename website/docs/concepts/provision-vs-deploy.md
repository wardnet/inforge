---
sidebar_position: 4
---

# Provisioning vs Deployment

inforge separates two distinct lifecycles: **provisioning** and **deployment**.

## Provisioning

**Provisioning** creates a VM's host-side scaffolding:

- The on-host folder `/srv/wardnet/<service>`
- An inforge-managed systemd unit `wardnet-<service>.service` whose `ExecStart` is `inforge-bootstrap`
- The service's secret-free `descriptor.yaml` and, for a secret-bearing service, its host-key-encrypted
  `credential.age` under `/etc/wardnet/services/<service>/` (the service fetches its secrets at runtime)

Provisioning is triggered by `inforge deploy` when the Pulumi state shows changes.
inforge **owns** the unit — it controls start, restart, and configuration. Service code
is never touched during provisioning.

## Deployment

**Deployment** delivers a service's payload to the provisioned host:

- The service repo's CI calls the `service-release` reusable workflow
- A gzip of the service's build artifacts is pushed via SCP
- The inforge-managed unit is restarted via SSH

Deployment is **independent** of provisioning. A service can deploy code dozens of
times without touching infrastructure. Infrastructure can change (e.g. resize a VM)
without triggering a code redeploy.

## Why separate?

- **Independent cadences**: infrastructure changes and code releases don't have to happen together
- **Ownership clarity**: inforge owns the runtime contract; service repos own the code
- **Simpler rollbacks**: rolling back code doesn't require re-provisioning infrastructure
- **Parallel operations**: multiple services can deploy simultaneously while infrastructure
  changes are serialised through Pulumi

## Target resolution

`service-release` resolves the deploy target (host DNS, folder, systemd unit) live from
the Pulumi stack in the platform repo at release time. No descriptor file needs to be
committed or kept in sync. The service repo only needs a `deployments/` directory that
names the platform repo and the artifact path per environment.

## Delivery types

| Type | Status | Description |
|------|--------|-------------|
| `raw` | Available | SSH-push of a gzip payload |
| `container` | Reserved | Pull-based container deployment (future) |
