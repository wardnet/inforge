package program

import "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

// The ADR-0042 lifecycle options, defined ONCE so the six delete-bearing
// command sites carry a greppable helper instead of six drifting copies of the
// same string literal and the same nine-line justification. The full story
// lives in docs/adr/0042-update-in-place-command-lifecycle.md and the rule
// delete-bearing-commands-have-no-triggers; the short version:
//
//   - A command whose Delete destroys host state carries NO Triggers: a
//     Triggers diff is a REPLACE, and DeleteBeforeReplace then runs the delete
//     RECORDED IN STATE by the previous deploy — how a routine change tears
//     down live state, and how a delete-script fix can never install itself
//     (the v6.1.0 outage). Idempotent script changes re-run IN PLACE via the
//     create/update diff instead.
//   - retiredTriggers makes removing a previously recorded trigger a ZERO-DIFF
//     migration: the `-triggers` diff is suppressed and the stale recorded
//     value stays in state, inert. Without it, the removal itself would force
//     one last replace — replaying the very delete being defused. (Verified
//     against pulumi-command v1.2.1.)
//   - ignoredSecretsChurn is the -secrets inversion: age encryption renders a
//     fresh ciphertext every run, so its create/update inputs diff on EVERY
//     deploy — suppressing them makes the command's deterministic Triggers the
//     sole change detector, and a no-op deploy genuinely `unchanged`. A
//     trigger-driven replace still runs the NEW write script (verified).
//
// IgnoreChanges takes free-string property paths and silently ignores a typo —
// these helpers are the only place the strings may appear.

// retiredTriggers suppresses the diff on a retired (no longer declared)
// Triggers input. Pair with every delete-bearing command that dropped its
// Triggers per ADR-0042.
func retiredTriggers() pulumi.ResourceOption {
	return pulumi.IgnoreChanges([]string{"triggers"})
}

// ignoredSecretsChurn suppresses the nondeterministic-ciphertext diff on the
// -secrets command's create/update inputs (ADR-0042 inversion).
func ignoredSecretsChurn() pulumi.ResourceOption {
	return pulumi.IgnoreChanges([]string{"create", "update"})
}
