---
sidebar_position: 2
---

# Compute

A **Compute** resource defines a virtual machine (or future cluster). inforge expands a single
spec into one or more VM instances based on `instance_count`.

A compute resource lives in a folder under `regional/compute/<name>/`:

```
regional/compute/bridge/
  manifest.yaml       # required — the compute spec
  cloud-init.sh       # optional — cloud-init script sidecar
```

## Schema

`manifest.yaml`:

```yaml
name: bridge             # required
instance_count: 1        # optional — defaults to 1; expands into bridge-01..bridge-NN
container: bridge        # required
provider: hetzner        # optional — inherits from inforge.yaml providers.compute if omitted
network: ingress         # required — name of the Network resource to join
size: SMALL              # required — resolved against the size table
image: ubuntu-24.04      # required — canonical OS image name
cloud_init: cloud-init.sh   # optional — path relative to the compute folder
kind: vm                 # optional — "vm" (default) | "cluster" (reserved)
deploy_user:             # optional — SSH deploy account provisioned at VM-init time
  name: deploy
firewall:                # optional — declarative inbound rules; omit to use defaults
  inbound:
    - proto: tcp
      port: 80
    - proto: tcp
      port: 443
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Resource name. |
| `instance_count` | int | No | How many VMs to create (default 1). Each gets a specKey `name-01` … `name-NN`. |
| `container` | string | Yes | Grouping label. |
| `provider` | string | No | Provider name (e.g. `hetzner`). Inherits from `inforge.yaml` `providers.compute` if omitted; explicit value takes precedence. A validation error is raised if neither is set. |
| `network` | string | Yes | Name of the Network resource to attach this VM to. |
| `size` | string | Yes | Size name from the size table: `SMALL`, `MEDIUM`, or `LARGE`. |
| `image` | string | Yes | OS image. See [supported images](#supported-images). |
| `cloud_init` | string | No | Path to a cloud-init script, relative to the compute **folder** (e.g. `cloud-init.sh`). Absolute paths are also accepted. |
| `kind` | string | No | `vm` (default; built) or `cluster` (k8s; reserved). |
| `deploy_user` | object | No | Deploy user provisioned at VM-init time. See [Deploy user](#deploy-user). |
| `firewall` | object | No | Declarative inbound firewall rules. See [Firewall rules](#firewall-rules). |

## Size table

The size table is the cloud-agnostic set of valid `size` names — `SMALL`, `MEDIUM`, `LARGE` by
default. It carries no cpu/memory; each provider maps a size name to a concrete machine type in its
[region realization](/providers/hetzner#server-types).

Override by placing `sizes.yaml` in `resources/<env>/`. The file is a YAML list of names and
**replaces** the default table wholesale:

```yaml title="resources/prd/sizes.yaml"
- SMALL
- MEDIUM
- LARGE
- XLARGE
```

## Supported images

| Value | Description |
|-------|-------------|
| `ubuntu-24.04` | Ubuntu 24.04 LTS |
| `ubuntu-22.04` | Ubuntu 22.04 LTS |
| `debian-12` | Debian 12 |

## Deploy user

When `deploy_user` is set, inforge provisions the named account at VM-init time with a first-boot
step. The step creates a login-shell user, installs the authorized key from `ssh.deployPublicKey`
in `variables.yaml`, and grants passwordless sudo. This happens **whether or not** the compute spec
declares a `cloud_init` template — with a template, the step is appended to it; without one, inforge
emits a minimal cloud-init that runs only this step.

Two conditions must hold for the deploy account to come up: `ssh.deployPublicKey` must be set (the
deploy fails fast if a `deploy_user` host has none), and any `cloud_init` template must be a **shell
script** (the provision step is appended as shell — a `#cloud-config` template would silently skip it).

```yaml
deploy_user:
  name: deploy
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Username for the SSH deploy account. |

The SSH key material is not set here — it comes from [`ssh.deployPublicKey`](/configuration/variables-yaml#ssh)
in `variables.yaml`. This keeps the key in one env-level location so rotating it only requires
updating `variables.yaml` and re-running `inforge deploy`.

## Cloud-init templates

Cloud-init scripts support these placeholders:

| Placeholder | Description |
|------------|-------------|
| `{{domain}}` | Fully-qualified domain for this VM instance |
| `{{deploy_public_key}}` | Deploy user's SSH public key (from `ssh.deployPublicKey`) |
| `{{deploy_user}}` | Deploy user name (from `deploy_user.name`; empty if not declared) |
| `{{instance}}` | Instance number (integer) |
| `{{manifest}}` | Assembled service manifest (YAML, secret-free) |

inforge adds one first-boot step automatically: user provisioning (creates the deploy user when
declared). With a `cloud_init` template it is appended to your script; without one inforge still emits
a minimal cloud-init carrying just this step, so a `deploy_user` host is provisioned even with no
template. Secrets are not a first-boot concern — each service fetches its own at runtime via
`inforge-bootstrap`.

## Firewall rules

The inbound rule set is **derived**, not hand-maintained:

- **SSH (22)** is always permitted (management access is never locked out).
- Every service's [ingress](./service#ingress) `listen` port on this host is opened automatically, plus
  **`:80`** when the host terminates TLS (nginx serves the ACME HTTP-01 challenge there).
- A cross-host service's route `target` ports, and every service's [`exposed_ports`](./service#exposed-ports),
  are opened **only to this host's private-network CIDR** — never the internet.
- Any rules in the `firewall.inbound` block are unioned on top — for **raw public** ports not fronted by
  nginx (a port a service binds directly, with no proxy).

Outbound traffic is always fully allowed. So `firewall.inbound` is for a raw **public** port; for a raw
port that must stay on the **private** network, declare it as a service
[`exposed_ports`](./service#exposed-ports) entry instead; ports behind nginx ingress open themselves.

```yaml title="regional/compute/bridge/manifest.yaml"
name: bridge
container: bridge
provider: hetzner
network: ingress
size: SMALL
image: ubuntu-24.04
firewall:
  inbound:
    # A raw UDP port a service binds directly (not nginx ingress).
    - proto: udp
      port: 51820
```

### Firewall rule fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `proto` | string | Yes | Protocol: `tcp`, `udp`, or `icmp`. |
| `port` | int or string | Yes | Port number (`80`) or range (`8000-9000`). Not used when `proto` is `icmp`. |

## Placement groups (Hetzner)

Every Hetzner VM automatically joins a **spread placement group**, which keeps servers on distinct
physical hosts to reduce correlated failure. This is on by default with no field to configure — inforge
creates the groups (`wardnet-<env>-<region>-pg-NN`) and assigns each server as it is provisioned.

Hetzner caps a spread group at **10 servers**, so servers bin-pack across as many groups as needed
(`-pg-01`, `-pg-02`, …). Assignment is deterministic in creation order, so re-running `inforge deploy`
keeps a server in the same group.

## Example

```yaml title="regional/compute/bridge/manifest.yaml"
name: bridge
instance_count: 1
container: bridge
network: ingress
size: SMALL
image: ubuntu-24.04
cloud_init: cloud-init.sh
deploy_user:
  name: deploy
```

```bash title="regional/compute/bridge/cloud-init.sh"
#!/bin/bash
set -euo pipefail

# Write the service manifest (inforge substitutes {{manifest}} at provision time).
mkdir -p /etc/wardnet
cat > /etc/wardnet/manifest.yaml << 'EOF'
{{manifest}}
EOF
```

inforge appends the deploy-user provisioning step automatically — no need to write `useradd` or
key-installation logic in your cloud-init template. Secrets are not a first-boot concern; each service
fetches its own at runtime via `inforge-bootstrap`.

## Outputs

| Output | Description |
|--------|-------------|
| `publicIP` | Public IPv4 address of the VM |
