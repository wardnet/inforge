---
sidebar_position: 4
---

# Provisioning vs Deployment

inforge separates two distinct lifecycles: **provisioning** and **deployment**.

## Provisioning

**Provisioning** creates a VM's host-side scaffolding:

- The on-host folder `/srv/wardnet/<service>`
- An inforge-managed systemd unit `wardnet-<service>.service`
- The SOPS/age-encrypted manifest at `/etc/wardnet/manifest.yaml`
- The bootstrap.yaml for first-boot secret redemption (when secrets are present)

Provisioning is triggered by `inforge deploy` when the Pulumi state shows changes.
inforge **owns** the unit — it controls start, restart, and configuration. Service code
is never touched during provisioning.

## Deployment

**Deployment** delivers a service's payload to the provisioned host:

- The service repo's CI calls the `deploy-raw` reusable workflow
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

## The deploy descriptor

After a successful `inforge deploy`, a **deploy descriptor** is written to `deploy/<env>.yaml`:

```yaml
environment: prd
targets:
  - service: api
    host_dns: bridge.use1.example.com
    folder: /srv/wardnet/api
    unit: wardnet-api.service
```

The `deploy-raw` workflow reads this file to know where to push payloads.

## Delivery types

| Type | Status | Description |
|------|--------|-------------|
| `raw` | Available | SSH-push of a gzip payload |
| `container` | Reserved | Pull-based container deployment (future) |
