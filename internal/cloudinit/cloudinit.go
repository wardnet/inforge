// Package cloudinit assembles a compute instance's cloud-init script: it
// substitutes the per-instance placeholders in a project's template and appends
// the inforge first-boot bootstrap step.
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

// Vars are the per-instance values substituted into a cloud-init template.
type Vars struct {
	Domain          string
	DeployPublicKey string
	Instance        int
	Manifest        string
}

// placeholders maps template tokens to their replacement values.
func (v Vars) placeholders() *strings.Replacer {
	return strings.NewReplacer(
		"{{domain}}", v.Domain,
		"{{deploy_public_key}}", v.DeployPublicKey,
		"{{instance}}", strconv.Itoa(v.Instance),
		"{{manifest}}", v.Manifest,
	)
}

// Assemble reads the template at absolutePath, substitutes the cloud-init
// placeholders, and appends the bootstrap step.
func Assemble(absolutePath string, vars Vars) (string, error) {
	template, err := os.ReadFile(absolutePath)
	if err != nil {
		return "", err
	}
	return Render(string(template), vars), nil
}

// Render substitutes the placeholders in a cloud-init template string and
// appends the bootstrap step. It is the pure core of Assemble.
func Render(template string, vars Vars) string {
	rendered := vars.placeholders().Replace(template)
	return rendered + "\n" + BootstrapScript()
}

// BootstrapScript returns the embedded first-boot bootstrap step.
func BootstrapScript() string {
	return bootstrapScript
}
