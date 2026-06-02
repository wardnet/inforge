---
sidebar_position: 2
---

# Compute

A **Compute** resource defines a virtual machine (or future cluster). inforge expands a single
spec into one or more VM instances based on `instance_count`.

## Schema

```yaml
name: bridge             # required
instance_count: 1        # optional — defaults to 1; expands into bridge-01..bridge-NN
container: bridge        # required
provider: hetzner        # required
network: ingress-01      # required — specKey of the Network to join
size: SMALL              # required — resolved against the size table
image: ubuntu-24.04      # required — canonical OS image name
cloud_init: bridge-01.cloud-init.sh   # optional — path relative to this file's directory
kind: vm                 # optional — "vm" (default) | "cluster" (reserved)
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
| `provider` | string | Yes | Must be `hetzner`. |
| `network` | string | Yes | specKey of the Network resource to attach this VM to. |
| `size` | string | Yes | Size name from the size table: `SMALL`, `MEDIUM`, or `LARGE`. |
| `image` | string | Yes | OS image. See [supported images](#supported-images). |
| `cloud_init` | string | No | Path to a cloud-init script, relative to the compute YAML file. |
| `kind` | string | No | `vm` (default; built) or `cluster` (k8s; reserved). |
| `firewall` | object | No | Declarative inbound firewall rules. See [Firewall rules](#firewall-rules). |

## Size table

| Size | vCPUs | Memory |
|------|-------|--------|
| `SMALL` | 2 | 4 GB |
| `MEDIUM` | 4 | 8 GB |
| `LARGE` | 8 | 16 GB |

Override by placing `sizes.yaml` in `resources/<env>/`. The file **replaces** the default table.

```yaml title="resources/prd/sizes.yaml"
- name: SMALL
  cpus: 2
  memory: 4
- name: MEDIUM
  cpus: 4
  memory: 16
```

## Supported images

| Value | Description |
|-------|-------------|
| `ubuntu-24.04` | Ubuntu 24.04 LTS |
| `ubuntu-22.04` | Ubuntu 22.04 LTS |
| `debian-12` | Debian 12 |

## Cloud-init templates

Cloud-init scripts support these placeholders:

| Placeholder | Description |
|------------|-------------|
| `{{domain}}` | Fully-qualified domain for this VM instance |
| `{{deploy_public_key}}` | Deploy user's SSH public key |
| `{{instance}}` | Instance number (integer) |
| `{{manifest}}` | Assembled service manifest (YAML, possibly SOPS-encrypted) |
| `{{bootstrap_doc}}` | Content of `bootstrap.yaml` (empty if no secrets) |

inforge appends the bootstrap step automatically to every cloud-init script.

## Firewall rules

When `firewall` is omitted, inforge allows only SSH (22) inbound. All other inbound ports must be declared explicitly. Outbound traffic is always fully allowed.

When `firewall` is present, only the declared `inbound` rules are applied — **plus SSH (22), which is always permitted** to preserve management access. Outbound traffic is always fully allowed regardless of what is declared.

```yaml title="resources/prd/us-east-1/compute/bridge.yaml"
name: bridge
container: bridge
provider: hetzner
network: ingress-01
size: SMALL
image: ubuntu-24.04
firewall:
  inbound:
    - proto: tcp
      port: 80
    - proto: tcp
      port: 443
```

### Firewall rule fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `proto` | string | Yes | Protocol: `tcp`, `udp`, or `icmp`. |
| `port` | int or string | Yes | Port number (`80`) or range (`8000-9000`). Not used when `proto` is `icmp`. |

## Example

```yaml title="resources/prd/us-east-1/compute/bridge-01.yaml"
name: bridge
instance_count: 1
container: bridge
provider: hetzner
network: ingress-01
size: SMALL
image: ubuntu-24.04
cloud_init: bridge-01.cloud-init.sh
```

```bash title="resources/prd/us-east-1/compute/bridge-01.cloud-init.sh"
#!/bin/bash
set -euo pipefail

# Install inforge-managed service
mkdir -p /srv/wardnet/bridge
cat > /etc/wardnet/manifest.yaml << 'EOF'
{{manifest}}
EOF
```

## Outputs

| Output | Description |
|--------|-------------|
| `publicIP` | Public IPv4 address of the VM |
