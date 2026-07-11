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

## Generating the CA material

The manifest **declares** that a CA should exist; it does not create it. The key material lives in the
environment's `pki.enc.yaml` store and is generated once, by hand:

```sh
inforge pki add <env> daemon-ca --topology root-only
```

Commit the resulting `pki.enc.yaml`. The root key is age-encrypted to the **CI recipient** (unlike a
two-tier cold root, which is encrypted to the offline root recipient) precisely so the deploy can
decrypt it and deliver it to the granted service.

:::warning A declared PKI is not a generated PKI
A `manifest.yaml` with no matching entry in `pki.enc.yaml` has **no material to grant**. `inforge
validate` rejects a grant on such a PKI:

```
grants[1].resource: pki resource "daemon-ca" is declared but has no material in pki.enc.yaml —
generate it with `inforge pki add <env> daemon-ca --topology root-only` and commit the store
```

Without this check the deploy would succeed while silently omitting the grant's env vars from the
service's descriptor, and the service would crash-loop at startup on a variable it requires.
:::

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
additionally publishes `{KEY}` (the root signing key, for issuance). A file-field output template must
contain only the placeholder — see below for why.

## How the material reaches the service

A PKI grant is a **file-field** grant, and that changes what the service actually receives:

1. At deploy, inforge decrypts the PKI's material from `pki.enc.yaml` and writes it into the service's
   host-key-encrypted `secrets.age`, alongside its ordinary secrets.
2. The service's `descriptor.yaml` advertises each output env var against a **file key** (e.g.
   `CA_KEY_PATH: pki/daemon-ca/key.pem`) — the descriptor never carries PEM content.
3. At boot the on-host agent decrypts `secrets.age`, writes each PEM into the service's tmpfs runtime
   directory (`/run/wardnet/<service>/…`, mode `0400`, owned by the service user), and sets the env var
   to **that path**.

So the env var the service reads holds a **path, not the key material** — which is why a file-field
template must be exactly `"{CERT}"` and never `"path={CERT}"`: the value is a filename, not a
composable string. The PEM never enters the service's environment, its argv, or the journal.

Rotating the material (regenerating the PKI and re-deploying) rewrites `secrets.age` and restarts the
service, exactly like a changed secret value.

## See also

- [Service — Grants](./service#grants) — how a grant is authored and materialized
- [`inforge pki`](/cli/pki) — the separate two-tier **mesh** PKI (not a grant target)
