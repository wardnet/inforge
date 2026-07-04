---
sidebar_position: 10
---

# PKI resource

A **PKI resource** is a standalone **root-only** certificate authority, authored as its own resource and
consumed by services through a **grant** — a `grants:` entry on a [service](./service) manifest that
targets `pki/<name>`. It is the daemon-facing CA: a service granted `ro` on it receives the CA
certificate (to **verify**), a service granted `rw` receives the root signing key (to **issue**).

:::caution Distinct from the mesh PKI
This is **not** the two-tier mesh PKI. The mesh PKI (service `pki:` membership, `CN=<scope>/<service>`
leaves) lives in `pki.enc.yaml` and is managed by [`inforge pki`](/cli/pki) — it is never a grant target.
A PKI **resource** is a separate, `root-only` CA reached only through a grant. The two never cross.
:::

A PKI resource lives in a folder under `regional/pki/<name>/` (or `global/pki/<name>/`):

```
regional/pki/daemon-ca/
  manifest.yaml       # required — the PKI resource spec
```

```yaml title="regional/pki/daemon-ca/manifest.yaml"
name: daemon-ca
container: pki
topology: root-only    # the only valid topology for a PKI resource
validity: 10y          # optional CA validity
```

## Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | PKI resource name (unique per scope); the grant target `pki/<name>` references it. |
| `container` | string | Yes | Logical container/grouping, like every resource. |
| `topology` | string | Yes | Must be **`root-only`**. Any other value is rejected — a two-tier mesh PKI is authored in `pki.enc.yaml`, not as a resource. |
| `validity` | string | No | CA validity (e.g. `10y`). |

## Granting access

A service reaches a PKI resource only through a grant on its manifest — never a `ref:`:

```yaml title="regional/service/signer/manifest.yaml"
grants:
  - resource: pki/daemon-ca
    permission: rw            # ro = CA cert (verify); rw = root signing key (issue)
    outputs:
      CA_CERT_PATH: "{CERT}"  # a file field must stand alone
      CA_KEY_PATH:  "{KEY}"   # only published for rw
```

A `ro` grant publishes the `{CERT}` field (the CA certificate, for verification); a `rw` grant
additionally publishes `{KEY}` (the root signing key, for issuance). Each resolves to a projected PEM's
on-host path, so a file-field output template must contain only the placeholder. A grant creates or
issues a credential (here, a minted cert), which is what distinguishes it from a `ref:` (which only
reads an existing output).

## See also

- [Service](./service) — grants are authored on the service manifest (`grants:`)
- [`inforge pki`](/cli/pki) — the separate two-tier **mesh** PKI (not a grant target)
