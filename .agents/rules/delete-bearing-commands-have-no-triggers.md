# Delete-bearing commands have no Triggers

A `remote.Command` / `local.Command` whose `Delete:` script destroys host state
(a role drop, a unit removal, a file delete, a timer teardown) must NOT set
`Triggers:`. A Triggers diff is a REPLACE, and with `DeleteBeforeReplace` the
engine runs the delete script **recorded in state by the previous deploy** — so
a release that fixes a delete script can never install its own fix (the replace
replays the broken one first: the v6.1.0 production outage), and a routine
content change tears down live state it is about to recreate.

Instead:

- The command's script must be **idempotent**, and a script change re-runs it
  **in place**: pulumi-command updates on a `create`/`update` input diff on its
  own — Triggers adds nothing but the replace hazard. If the update must move a
  running consumer onto the new state (a new binary, a changed unit), the script
  itself ends with the restart (`systemctl try-restart`), not the resource
  lifecycle.
- When retiring an existing `Triggers:`, always add
  `pulumi.IgnoreChanges([]string{"triggers"})` — removing the input alone diffs
  `-triggers` and forces one last replace (verified against pulumi-command
  v1.2.1); ignoring it leaves the stale recorded value in state, inert.
- The one sanctioned inversion is a **nondeterministic script** (the `-secrets`
  command: age encryption produces fresh ciphertext every render). There the
  churn is in `create`/`update`, so the command sets a **deterministic**
  `Triggers:` as the sole change detector, ignores
  `IgnoreChanges([]string{"create","update"})` — and must be **delete-free**:
  its trigger-driven replaces are routine, so a delete would run on every real
  change. Its teardown belongs to a companion command whose delete only runs on
  true removal (`serviceDeprovisionScript`).

See ADR-0042 and `program/adr0042_test.go` for the enforced contracts.
