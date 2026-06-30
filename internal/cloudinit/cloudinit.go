// Package cloudinit assembles a compute instance's cloud-init script: it
// substitutes the per-instance placeholders in a project's template and appends
// the inforge first-boot user-provisioning step. Secret delivery is no longer a
// first-boot concern — secrets are fetched at runtime by inforge-bootstrap — so
// the former SOPS/age re-key bootstrap step has been retired.
package cloudinit

import (
	_ "embed"
	"os"
	"strconv"
	"strings"
)

// provisionScript is the first-boot user-provisioning step appended to every
// assembled cloud-init script.
//
//go:embed provision.sh
var provisionScript string

// Vars are the per-instance values substituted into a cloud-init template.
type Vars struct {
	Domain          string
	DeployPublicKey string
	// DeployUser is the name of the deploy user to provision. Empty when the
	// compute spec declares no deploy_user.
	DeployUser string
	Instance   int
	Manifest   string
}

// placeholders maps template tokens to their replacement values.
func (v Vars) placeholders() *strings.Replacer {
	return strings.NewReplacer(
		"{{domain}}", v.Domain,
		"{{deploy_public_key}}", v.DeployPublicKey,
		"{{deploy_user}}", v.DeployUser,
		"{{instance}}", strconv.Itoa(v.Instance),
		"{{manifest}}", v.Manifest,
	)
}

// Assemble reads the template at absolutePath, substitutes the cloud-init
// placeholders, and appends the inforge-managed first-boot steps.
func Assemble(absolutePath string, vars Vars) (string, error) {
	template, err := os.ReadFile(absolutePath)
	if err != nil {
		return "", err
	}
	return Render(string(template), vars), nil
}

// Render substitutes the placeholders in a cloud-init template string and
// appends the inforge-managed first-boot user-provisioning step. It is the pure
// core of Assemble.
func Render(template string, vars Vars) string {
	r := vars.placeholders()
	return r.Replace(template) + "\n" + r.Replace(ProvisionScript())
}

// ProvisionScript returns the embedded first-boot user-provisioning step.
func ProvisionScript() string {
	return provisionScript
}

// provisionShebang is the interpreter line a project cloud_init template would
// normally supply. ProvisionOnly prepends it so the standalone provisioning
// user-data is a script cloud-init recognises and executes.
const provisionShebang = "#!/bin/bash"

// ProvisionOnly renders a complete cloud-init user-data script that runs ONLY
// the inforge first-boot provisioning step (deploy-user creation), for compute
// specs that declare a deploy_user but supply no project cloud_init template.
// It mirrors what Render produces from a bare shebang template, so the deploy
// user is created identically whether or not a project supplies its own
// cloud-init. Without it, such a host boots with only root reachable and every
// deploy_user SSH command fails with "[none publickey]".
func ProvisionOnly(vars Vars) string {
	return Render(provisionShebang, vars)
}
