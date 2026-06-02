# Declarative user provisioning: deploy user on compute, service user on service

User provisioning — previously embedded in project-supplied cloud-init scripts — is now declared
in the resource model and applied by inforge at the appropriate lifecycle stage.

## Deploy user (`compute.deploy_user`)

The deploy user is the SSH account the release workflow authenticates as to push payloads to a
VM. It is a VM-level concern: one deploy user per compute instance, shared by all services
running on that host.

```yaml
deploy_user:
  name: deploy
```

inforge appends a first-boot provisioning step (after the project cloud-init, before the secret
bootstrap) that creates this user with a login shell and installs the authorised key from the
environment-level `ssh.deployPublicKey`. When `deploy_user` is absent no user is created and the
`{{deploy_user}}` placeholder in cloud-init templates resolves to the empty string.

The SSH key material stays in `variables.yaml` under `ssh.deployPublicKey` — it is
environment-level configuration, not per-compute. The compute spec declares only the username.

## Service user (`service.user`)

The service user is the no-login system account a raw service's systemd unit runs as. It is a
service-level concern: each service declares its own user independently.

```yaml
user: wardnet
```

When set, inforge:
1. Emits a `User=<name>` directive in the inforge-managed systemd unit.
2. Runs `useradd --system --shell /usr/sbin/nologin <name>` via SSH on first deploy (idempotent).

When absent, no user is created and no `User=` directive is emitted. The field is only meaningful
for `type: raw` services; container services manage their user inside the image.

## Why separate the two

The deploy user must exist before the first deployment can happen (it is the SSH account doing
the deploying), so it must be provisioned at VM init time via cloud-init. The service user only
needs to exist before the service starts, so it can be provisioned at deploy time over SSH — no
VM reprovision required to add or change it.

## Replaces

User creation in project-supplied cloud-init templates and the hardcoded `User=wardnet` line in
the inforge-managed systemd unit. SSH key rotation no longer requires a full VM reprovision.
