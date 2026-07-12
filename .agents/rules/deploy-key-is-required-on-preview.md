# The deploy SSH private key is required on preview, not just deploy

A run without the deploy key is refused (`program.resolveDeployKey`). It is tempting to treat the
key as deploy-only — a preview never opens an SSH connection, so withholding it *looks* free — but
the key is an **input** to every `remote.Command` resource (`internal/remote.Connection` sets
`ConnectionArgs.PrivateKey`). A keyless run registers all of them with `privateKey: ""`, Pulumi diffs
that empty string against the real key in state, and previews a **spurious update for every host
command**.

This is not hypothetical. `wardnet-infrastructure` PR #46 previewed `~ 52 to update` on a change
whose deploy actually replaced 6 resources: its CI preview job deliberately omitted
`INFORGE_DEPLOY_PRIVATE_KEY` on exactly the reasoning above. Every one of the 52 phantom diffs was a
`command:Command`; no cloud resource diffed — the fingerprint of a connection-level diff.

## Applies to

`program.Run` (via `resolveDeployKey`), `internal/remote.Connection`, and the backstop guards in
`providers/hetzner/{tls,mesh}.go`. Any CI workflow that runs `inforge preview` must pass the key
(stack config `deploy_private_key` or `INFORGE_DEPLOY_PRIVATE_KEY`) exactly as the deploy job does.

Do not "fix" a keyless preview by hiding the credential from the diff:

- `IgnoreChanges(["connection.privateKey"])` **does not work.** pulumi-command wraps the whole
  connection object in a secret (`remote.NewCommand`: `args.Connection = pulumi.ToSecret(...)`), and
  Pulumi's `PropertyPath.Get` cannot descend into a secret value — the path silently never resolves.
- `IgnoreChanges(["connection"])` (the whole connection) *does* work and is worse: after a deploy-key
  rotation Pulumi replays the stale key from state and every SSH fails, and a rebuilt host (new
  public IP) no longer re-runs its provisioning commands — the connection changing is precisely what
  forces that re-run today (see the `DeleteBeforeReplace` note in `provisionService`).

## Why

A preview whose numbers don't match the deploy is worse than no preview: it trains reviewers to
ignore the diff. Fidelity requires preview to compute the **same inputs** as deploy, which means it
needs the same key. Failing loudly when it is absent makes the mis-report structurally impossible
rather than dependent on someone remembering an env var in a workflow file.
