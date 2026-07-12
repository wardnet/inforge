package program

import "fmt"

// resolveDeployKey picks the deploy SSH private key from stack config
// (deploy_private_key, preferred) or the INFORGE_DEPLOY_PRIVATE_KEY environment
// variable, and fails when neither carries it.
//
// The key is required on EVERY run — preview as much as deploy — even though a
// preview opens no SSH connection. It is an INPUT to every remote.Command
// resource (internal/remote.Connection), so a keyless run registers all of them
// with an empty privateKey; Pulumi diffs that empty string against the real key
// recorded in state and plans a spurious update for every single host command.
// That is not hypothetical: a wardnet-infrastructure PR whose deploy really
// changed 6 resources previewed as "52 to update" because its CI preview job
// deliberately withheld the key on the (incorrect) grounds that a preview runs
// no remote command.
//
// Failing here rather than proceeding with an empty key is the point: a preview
// that silently mis-reports what a deploy will do is worse than one that
// refuses to run. Ignoring the credential in the diff is NOT an alternative —
// pulumi-command marks the whole connection object secret, and Pulumi's property
// paths cannot descend into a secret, so only the entire connection can be
// ignored. Doing that would replay a stale key from state after a key rotation
// and would hide the command re-runs a host rebuild must trigger.
func resolveDeployKey(fromConfig, fromEnv string) (string, error) {
	if fromConfig != "" {
		return fromConfig, nil
	}
	if fromEnv != "" {
		return fromEnv, nil
	}
	return "", fmt.Errorf("no deploy SSH private key: set the deploy_private_key stack config or the " +
		"INFORGE_DEPLOY_PRIVATE_KEY environment variable. It is required for preview as well as deploy — " +
		"the key is an input to every remote command resource, so a keyless run reports a spurious update " +
		"for every host command instead of the changes a deploy would actually make")
}
