# A grant output template for a file field must contain only the placeholder, no surrounding text

A file-field output template (e.g. `{CERT}`, `{KEY}`) must be exactly the placeholder and nothing
else — no prefix, no suffix, no other characters. The value resolves to an on-host PEM path (via the
descriptor `files:` projection), not a composable string. Mixing literal text with a file-field
placeholder is rejected by `validate.checkGrants` and is architecturally wrong: the result would be
an unparseable path. Value-field templates (e.g. `{USER}:{PASSWORD}@{HOST}:{PORT}/{DBNAME}`) may
freely compose literals with placeholders — the two categories must never appear in the same template.

## Applies to

Any code that parses or validates grant `outputs:` entries (`internal/validate`, `internal/grant`),
and any manifest author writing a `grants:` block for a PKI resource grant.

## Example

```yaml
# CORRECT: file fields stand alone
grants:
  - resource: pki/daemon
    permission: ro
    outputs:
      DAEMON_CA_CERT: "{CERT}"      # ← bare placeholder; resolves to /run/service/daemon-ca.pem

# WRONG: literal mixed with file field
grants:
  - resource: pki/daemon
    permission: ro
    outputs:
      DAEMON_CA_CERT: "path={CERT}" # ← rejected by checkGrants; {CERT} is a path, not a string token
```
