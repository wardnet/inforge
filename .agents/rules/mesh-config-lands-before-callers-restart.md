# A mesh caller must never restart before every callee's allow-map is live

A service's restart (`deliverServiceSecrets`, which ends in `reloadOrRestartScript`) must
carry an explicit `DependsOn` every mesh proxy's **nginx-reload** resource — across **every**
scope, not just its own.

`MeshProvider.Realize` returns that reload resource for exactly this purpose. Do not discard
it, and do not depend on the config *write* instead: the write only puts bytes on disk, and
the allow-map is not live until nginx reloads.

## Why declaration order is not enough

`program.Run` declares `realizeMesh` before `provisionServices`, and realizes the global scope
before the regional one. **Neither fact orders anything.**

**Pulumi is a DAG, not a script.** The loop only *declares* resources; the engine executes them
in dependency order and runs anything unordered **concurrently**. A side-effecting
`remote.Command` produces no output that a later resource consumes, so there is no implicit
edge between a callee's mesh reload and a caller's restart — they race.

The "global slice realizes first" comment is true only for **data** dependencies (a regional
scope awaiting a global compute output). It says nothing about an SSH command whose result
nobody reads.

## What breaks

A mesh caller comes up on its new configuration while the callee's mesh proxy still holds the
**previous** allow-map. The caller presents a valid leaf whose identity `$mesh_allow_<svc>`
does not yet list, so `meshnginx`'s defence-in-depth guard correctly returns **403** — on every
call — until the reload lands.

Observed in prd: `ddns` restarted at `15:25:10` and `tenants`' mesh proxy reloaded at
`15:25:11`. In that window every `ddns → tenants` call was 403'd. It self-heals on the caller's
next tick, so it presents as a burst of alarming warnings rather than a lasting outage — but a
**brand-new** service is genuinely non-functional until the callee reloads, and the noise cost
a real investigation.

It also has a second-order cost: it forces the built-in **Mesh Calls Failing** alert to be a
slow Warning rather than a fast page, because otherwise every deploy would fire it.

## Applies to

`program.provisionServices` → `deliverServiceSecrets` · `program.realizeMesh` (accumulates the
reloads across scopes) · `types.MeshProvider.Realize` · `providers/hetzner.HetznerMesh.Realize`.

**Any new resource that restarts a mesh member must take the same dependency.** Adding one
without it silently re-opens the window.
