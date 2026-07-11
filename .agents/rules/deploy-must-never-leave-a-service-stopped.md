# A deploy must never leave a service stopped, and must never hide a failed restart

Any code path that can STOP a running service must also be the path that brings it back. Two rules
follow, and both are load-bearing:

1. **`serviceProvisionScript` uses `systemctl enable --now`, not `enable`.** That script is the CREATE
   half of a REPLACE whose DELETE half (`serviceDeprovisionScript`) runs `disable --now` + `rm -f
   <unit>`. The provision resource's `Triggers` cover the inforge-agent version, so **every inforge
   release replaces it** — meaning every release tears a running service down. Enabling without
   starting leaves it dead.

   It is safe: the unit is enabled and `WantedBy=multi-user.target`, so systemd already starts it
   unprompted on every reboot, and `ConditionPathExists=<exec>` makes that a clean no-op (exit 0) before
   any release has landed a binary. `enable --now` walks the same path.

   Do **not** rely on the delivery command's restart to rescue this: when only the agent version changes,
   the descriptor is unchanged and Pulumi skips delivery entirely.

2. **`reloadOrRestartScript` must not swallow failures** (`2>/dev/null || true`). A failed restart is
   the difference between a dead service and a green deploy. The original justification — a first deploy
   whose ExecStart payload does not exist yet — is already handled by `ConditionPathExists`.

3. **Every delivery command must `DependsOn` its service's provision command.** Delivery ends in
   `reloadOrRestartScript`; without the edge it can race the `rm -f <unit>` of a provision replace and
   fail with "Unit not found". Depend on the unit explicitly — not transitively via `hostKey`.

## Applies to

`program.provisionService` / `serviceProvisionScript` / `serviceDeprovisionScript` /
`reloadOrRestartScript` / `deliverServiceSecrets` / `deliverServiceDescriptor`.

## Why

This is not hypothetical. On wardnet prd's 5.5.1 → 5.5.2 deploy all three defects lined up: the agent
version bumped → the provision resource was replaced → `disable --now` stopped `tenants` and removed its
unit → the unordered delivery command's `systemctl restart` ran inside that window, failed with "Unit not
found", and was swallowed by `|| true` → provision then rewrote the unit and `enable`d it **without
starting it**. Result: unit enabled, service dead, `NRestarts: 0`, no error anywhere, and a deploy that
reported **success**. The API stayed down until it was started by hand.
