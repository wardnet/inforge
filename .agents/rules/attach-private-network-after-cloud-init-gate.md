# A host's private network is attached after its cloud-init readiness gate, never inline at server-create

The Hetzner compute provider provisions a server in **two** steps, not one:

- `HetznerCompute.Create` (`providers/hetzner/compute.go`) creates the server **without** any
  `Networks:` attachment and records it in `h.servers` (keyed by `naming.SpecKey(spec.Name, instance)`).
  It returns `ComputeOutputs.PrivateIP` **empty**.
- `HetznerCompute.AttachNetwork` creates a standalone `hcloud.ServerNetwork` (server ↔ subnet)
  `DependsOn` the host's cloud-init readiness gate, and returns the assigned private IP.

`program.attachPrivateNetworks` (`program/program.go`) drives step two: it runs once per scope, right
after the per-scope `gates` map is created and **before** `provisionApps`/`realizeIngress`, creating each
host's gate (via the shared, memoized `cloudInitGate`) then calling `AttachNetwork` gated on it, and
writes the returned private IP back into `computeOutputs[scope][specKey].PrivateIP`.

## The invariant

**Never attach a host's private network inline in `Create` (no `Networks:` on `hcloud.ServerArgs`).**
The attach must be a separate `hcloud.ServerNetwork` that depends on the cloud-init gate.

## Why

On the Hetzner images inforge uses (Ubuntu 24.04.4 and 26.04, cloud-init **>= 25.3**), cloud-init itself
configures the private NIC via the Hetzner datasource. On the **first boot** of a server that was created
**with** its private network, the hot-added private NIC has not yet been enumerated by the kernel when
cloud-init processes the datasource's `network-config` at the `init-local` stage. The interface therefore
resolves to a **null name**, which:

1. fails cloud-init's own `network-config-v1` schema validation, and
2. crashes cloud-init in `cloudinit/net/__init__.py:sys_dev_path` —
   `TypeError: can only concatenate str (not "NoneType") to str`,

aborting the `init-local` stage. The failure is **sticky**: `cloud-init status` reports `error` for that
boot even after networking recovers, so `systemd-networkd-wait-online` also times out (~120s). inforge's
root cloud-init readiness gate (`cloud-init status --wait`) then fails, and the whole `inforge deploy`
aborts **before any host-level command runs** — the deploy dies at the gate, not one hop late.

The race is non-deterministic (it depends on NIC-enumeration timing at first boot), so it presents as
"sometimes the gate passes." Verified on a throwaway 26.04 host: a server created with **no** private
network boots `status: done`; attaching the network afterward self-configures the NIC via the image's
**hotplug** path (`install_hotplug` in `90-hetznercloud.cfg` writes `50-cloud-init.yaml`) with cloud-init
staying `done`; and every subsequent reboot is clean because the NIC is then a **persistent** device
enumerated before `init-local`. Deferring the attach past the gate is therefore the fix — and it needs no
host-side NIC configuration (do **not** add a competing netplan step; it would duplicate/conflict with
cloud-init's hotplug-written config).

Downgrading the image does **not** help: 24.04.4 backported the same cloud-init 25.3, so both LTS images
carry the bug. The lever is *when* the network is attached, not *which* image.

## Applies to

`providers/hetzner/compute.go` (`Create` must not set `Networks:`; `AttachNetwork` owns the attach),
`internal/types/types.go` (the `ComputeProvider` contract documents both steps), and
`program/program.go` (`attachPrivateNetworks` must run before `realizeIngress`, the sole `PrivateIP`
consumer). A future compute provider implementation must honour the same two-step contract. If a new pass
starts consuming `PrivateIP`, it must run **after** `attachPrivateNetworks`, not between `Create` and it.
