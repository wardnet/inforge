# `inforge pki renew` must never invoke the Pulumi program

Leaf certificate renewal is intentionally decoupled from infrastructure deployment.
Running Pulumi inside `renew` would push un-shipped infra changes whenever cert renewal
is scheduled, turning a safe cron operation into an unintended deploy gate.

## Applies to

`cmd/inforge/pki.go` and any future subcommand added under `inforge pki renew`.
Also applies if renew is factored into a library function — the Pulumi SDK must not
be imported from the renewal path.

## Example

```go
// WRONG — renew must not call auto or up
pulumi.RunWithContext(ctx, func(pCtx *pulumi.Context) error { ... })

// RIGHT — renew writes directly to the provider via infisical.CertWriter
writer.Write(ctx, svc.Container, svc.Name, meshcert.MtlsDir, files)
```
