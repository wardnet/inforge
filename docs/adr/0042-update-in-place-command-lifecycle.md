---
status: accepted
date: 2026-07-14
---

# Update-in-place command lifecycle: Triggers retired from delete-bearing commands

The v6.1.0 deploy took production down. The proximate failure was a Postgres role
drop, but the structural cause was a lifecycle convention this ADR retires: every
host-mutating `remote.Command` carried `Triggers: [its own script]`, so **every
script change was a REPLACE** — and with `DeleteBeforeReplace`, every replace first
ran the delete script **recorded in Pulumi state by the previous deploy**.

Two consequences follow, and we hit both:

1. **A fix to a delete script can never install itself.** The release that ships
   the fixed delete also changes the script (the trigger), forcing a replace that
   replays the *old, broken* delete before the new one is recorded. (#225 → the
   v6.1.0 outage; v6.1.1 was the state-migration release that broke the loop.)
2. **Routine changes tear down live state.** The role mint dropped and re-minted a
   live credential on any SQL change; the service provision ran
   `disable --now` + `rm -f <unit>` on every host on **every release** (the agent
   version is in the script), leaving a window where an abort strands the fleet.
   The `-secrets` delete removed `descriptor.yaml` + `secrets.age` on every
   descriptor change — which is exactly what left every service one restart from
   death when the v6.1.0 run aborted mid-flight.

## The decision

**A command whose delete destroys host state carries no Triggers.** Its script must
be idempotent; a script change re-runs it **in place** (pulumi-command updates on a
`create`/`update` input diff by itself — verified against pulumi-command v1.2.1,
our pinned SDK). The delete script runs **only on true removal from the manifest**,
which is the only moment it was ever meant for. Where the update must move a
running consumer onto new state, the script ends with the restart
(`systemctl try-restart` in `serviceProvisionScript`) — the lifecycle no longer
smuggles restarts through replace stop/start cycles.

Applied to: the per-service DB-role mint, the monitor-role mint, the service
provision (`-provision`), the static host-file writes (`writeHostFile`: descriptor,
mesh descriptor), and the backup timer.

**Migration is zero-diff.** Removing the `Triggers` input alone diffs `-triggers`
and forces one final replace — replaying the very deletes we are defusing. Every
retired command therefore adds `pulumi.IgnoreChanges([]string{"triggers"})`: the
diff is suppressed, the stale recorded trigger stays in state, inert. (Verified:
unchanged / update-still-flows / replace-runs-new-create, all against v1.2.1.)

## The inversion: `-secrets`

The secrets write is the one command whose script is **nondeterministic**: age
encryption produces fresh ciphertext every render. Its `create`/`update` inputs
therefore diff on *every* deploy — which means, pre-ADR, **every deploy rewrote
`secrets.age` and reload-or-restarted every secret-bearing service**. That silent
restart storm was the engine behind the per-deploy mesh 403 races (#226) and the
"everything always changed" previews.

For this command the convention inverts:

- `Triggers: [descriptor, safeTrigger(plaintextHash)]` stays as the **sole,
  deterministic** change detector;
- `IgnoreChanges(["create","update"])` suppresses the ciphertext churn — a no-op
  deploy is now genuinely `unchanged`;
- a real change replaces the resource and the replacement runs the **new** write
  script (verified);
- the command is **delete-free**: since its replaces are routine, a delete here
  would run on every real change. On-host teardown (unit + descriptor +
  secrets.age) is consolidated in `serviceDeprovisionScript` — the unit's delete —
  so the agent inputs live exactly as long as the unit does.

One self-heal is deliberately lost: the old churn re-encrypted `secrets.age` to a
rotated SSH host key on the next deploy by accident. A host key rotation without a
host replacement now needs a forced rewrite (any secret/descriptor change, or a
state edit); a host *replacement* changes the command's `Connection` and rewrites
as before.

## Consequences

- A deploy where nothing changed reports **nothing changed** — the precondition
  for the preview/deploy output rework (legibility over a quiet baseline).
- Services restart only when something they consume changed: their unit, the
  agent binary, their descriptor, or a secret. Not on every deploy.
- The #225 group-role mint redesign re-lands safely: a mint-SQL change is now an
  in-place idempotent re-mint.
- The rule `delete-bearing-commands-have-no-triggers` and
  `program/adr0042_test.go` enforce the convention; `safeTrigger` remains only
  where a trigger is the sole detector over secret material.
