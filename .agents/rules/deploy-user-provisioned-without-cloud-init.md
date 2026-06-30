# A declared deploy_user is provisioned even when the compute spec has no cloud_init

The first-boot step that creates a host's `deploy_user` (`internal/cloudinit/provision.sh`: `useradd`
+ install `ssh.deployPublicKey` into the user's `authorized_keys` + passwordless sudo) is the **only**
thing that makes the deploy account reachable. Hetzner injects the server's `SshKeys` into **root**
only — never into the deploy user — so without this step the deploy user does not exist and every
`deploy_user` SSH command (`*-hostkey`, `*-provision`, ingress `*-install`) fails with
`ssh: unable to authenticate, attempted methods [none publickey]`. The root-only `*-cloudinit-ready`
gate still passes, so the failure surfaces one hop late and looks like a key problem when it is not.

## The invariant

`HetznerCompute.Create` (`providers/hetzner/compute.go`) must set the server's `UserData` whenever a
`deploy_user` is declared, **independent of** whether `spec.CloudInit` is set:

- `spec.CloudInit != ""` → `cloudinit.Assemble(template, vars)` (the template + the appended provision
  step).
- `spec.CloudInit == "" && deploy_user declared` → `cloudinit.ProvisionOnly(vars)` — a standalone,
  shebang-prefixed cloud-init that runs **only** the provision step.
- neither → no `UserData` (a host needing no first-boot work must not be handed user-data, since
  `user_data` is ForceNew on the Hetzner server and would otherwise force a needless replacement).

Do **not** re-nest the provision step inside an `if spec.CloudInit != ""` block — that is the exact
regression this rule exists to prevent (a host declaring `deploy_user` but no `cloud_init` booted with
only root reachable). `cloudinit.Render`/`Assemble` always append the provision step; `ProvisionOnly`
exists so the no-template path produces a valid script (cloud-init needs the shebang a project template
would otherwise supply).

A declared `deploy_user` also requires a non-empty `ssh.deployPublicKey`: `provision.sh` no-ops without
the key, so emitting user-data anyway would replace the server (`user_data` is ForceNew) yet leave the
account uncreated. `Create` fails fast (outside dry-run) when `deployUser != "" && deployPublicKey ==
""` rather than producing that silent gap.

This obligation is provider-agnostic — the program's host-level SSH passes (service provisioning,
observability, app seeding, ingress install) all assume a declared `deploy_user` host is reachable. It
is documented on the `types.ComputeProvider` interface so a future provider implementation re-honours it
instead of silently reintroducing the gap.

## Applies to

`providers/hetzner/compute.go` (`Create` UserData assembly); `internal/cloudinit/cloudinit.go`
(`Render`, `Assemble`, `ProvisionOnly`, `provision.sh`); `internal/types` (`DeployUserSpec`,
`ComputeSpec.CloudInit`). Tests:
`providers/hetzner/compute_test.go::TestComputeCreateProvisionsDeployUserWithoutCloudInit` and
`internal/cloudinit/cloudinit_test.go::TestProvisionOnly`.

## Why

`deploy_user` is an inforge-managed concern: inforge SSHes in as that account to realize every
host-level resource (service units, ingress nginx, the observability collector). Coupling its creation
to the presence of an optional, project-authored `cloud_init` template means a perfectly valid manifest
(`deploy_user` set, no template) silently produces an unreachable host that fails deep in the deploy
after the servers are already billed. `inforge validate` requires a `deploy_user` for service hosts but
cannot see this gap — the contract is only honoured if `Create` always provisions a declared deploy user.
