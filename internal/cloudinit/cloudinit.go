// Package cloudinit assembles a compute instance's cloud-init script: it
// substitutes the per-instance placeholders in a project's template and appends
// the inforge first-boot steps (user provisioning, then secret bootstrapping).
package cloudinit

import (
	_ "embed"
	"os"
	"strconv"
	"strings"
)

// bootstrapScript is the first-boot secret-bootstrap step appended to every
// assembled cloud-init script.
//
//go:embed bootstrap.sh
var bootstrapScript string

// provisionScript is the first-boot user-provisioning step appended to every
// assembled cloud-init script before the bootstrap step.
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
	// BootstrapDoc is the YAML content of bootstrap.yaml written to the VM at
	// first boot when the manifest contains secret values. Empty when the
	// manifest has no secrets (no bootstrap needed).
	BootstrapDoc string
}

// placeholders maps template tokens to their replacement values.
func (v Vars) placeholders() *strings.Replacer {
	return strings.NewReplacer(
		"{{domain}}", v.Domain,
		"{{deploy_public_key}}", v.DeployPublicKey,
		"{{deploy_user}}", v.DeployUser,
		"{{instance}}", strconv.Itoa(v.Instance),
		"{{manifest}}", v.Manifest,
		"{{bootstrap_doc}}", v.BootstrapDoc,
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
// appends the inforge-managed first-boot steps. It is the pure core of Assemble.
func Render(template string, vars Vars) string {
	r := vars.placeholders()
	return r.Replace(template) + "\n" + r.Replace(ProvisionScript()) + "\n" + r.Replace(BootstrapScript())
}

// BootstrapScript returns the embedded first-boot bootstrap step.
func BootstrapScript() string {
	return bootstrapScript
}

// ProvisionScript returns the embedded first-boot user-provisioning step.
func ProvisionScript() string {
	return provisionScript
}
