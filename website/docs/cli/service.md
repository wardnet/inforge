---
sidebar_position: 7
---

# inforge service

Operate on already-deployed services over SSH.

## `inforge service restart`

```
inforge service restart <env> <service> [--ssh-key <path>] [--stack-config <path>]
```

Restarts the service's systemd unit on **every host it deploys to**, then verifies the unit is
active. Useful for a manual bounce independent of any change — `inforge deploy` already restarts a
service automatically whenever its resolved secrets change (the push to `secrets.age` is hash-gated,
so a [secret value change](/cli/secret) takes effect on the next deploy without a separate restart).

Targets (host DNS, unit name, SSH user) come from the infra stack's `deployDescriptor` output — the
same source [`inforge releases deploy`](/cli/releases) delivers by — so restart can never disagree
with deploy about where a service lives. Connecting to the stack needs the same backend environment
as a deploy (e.g. `PULUMI_CONFIG_PASSPHRASE`, plus `CLOUDFLARE_ACCOUNT_ID` and the R2 credentials for
an `r2://` backend).

The SSH key is `--ssh-key` or the `INFORGE_DEPLOY_KEY` environment variable (a file path), connecting
as the host's deploy user — the same account `releases deploy` uses.

## Flags

| Flag | Description |
|------|-------------|
| `--ssh-key` | Path to the SSH deploy key (overrides `INFORGE_DEPLOY_KEY`). |
| `--stack-config` | Path to the infra stack config file (default: `inforge.<env>.yaml`). |
