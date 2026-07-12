// Package cloudinit assembles a compute instance's cloud-init script: it
// substitutes the per-instance placeholders in a project's template and appends
// the inforge first-boot user-provisioning step. Secret delivery is no longer a
// first-boot concern — secrets are fetched at runtime by inforge-agent — so
// the former SOPS/age re-key bootstrap step has been retired.
package cloudinit

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"
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

// placeholders maps template tokens to their replacement values. The deploy-user
// and deploy-key tokens are substituted into single-quoted shell assignments in a
// root-run first-boot script (provision.sh), so their values are escaped for that
// context: between single quotes every byte is literal except the quote itself, so
// closing and reopening the quoted run around each embedded quote is the whole
// escape — the value can then carry newlines, $(...) or backticks and stay data.
func (v Vars) placeholders() *strings.Replacer {
	return strings.NewReplacer(
		"{{domain}}", v.Domain,
		"{{deploy_public_key}}", escapeSingleQuoted(v.DeployPublicKey),
		"{{deploy_user}}", escapeSingleQuoted(v.DeployUser),
		"{{instance}}", strconv.Itoa(v.Instance),
		"{{manifest}}", v.Manifest,
	)
}

// escapeSingleQuoted makes s safe to interpolate BETWEEN single quotes in a POSIX
// shell script: '\'' ends the quoted run, emits a literal quote, and reopens it.
func escapeSingleQuoted(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// deployUserRe is the accepted deploy-user charset: a portable login name. The name
// is interpolated into file paths (/home/<user>, /etc/sudoers.d/<user>) and into a
// sudoers rule, so it is restricted rather than merely escaped.
var deployUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// deployKeyRe is the accepted deploy public key form: one OpenSSH authorized_keys
// entry ("<type> <base64>[ <comment>]"). A multi-line value would otherwise install
// extra keys into the deploy account's authorized_keys. The comment excludes the
// single quote — the ONE byte escapeSingleQuoted rewrites — so escaping is an
// identity on every value that passes Validate. That matters because placeholders()
// also substitutes the key into the PROJECT's cloud_init template, which need not be
// a single-quoted shell run (a #cloud-config template embeds it in YAML): a quote in
// the comment would be escaped for a context that template does not have, and the
// key would land there as "…'\''…". A key comment is a label; it has no need of one.
var deployKeyRe = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]+=*( [^\n']*)?$`)

// Validate rejects deploy material provision.sh must not be handed: a deploy user
// outside the login-name charset and a public key that is not a single-line OpenSSH
// entry. Escaping keeps a hostile value from executing, but the account it would
// create — or the keys it would install — is still not what the operator declared,
// so the server must not boot with it.
func (v Vars) Validate() error {
	if v.DeployUser != "" && !deployUserRe.MatchString(v.DeployUser) {
		return fmt.Errorf("deploy_user name %q is not a valid login name (allowed: %s)", v.DeployUser, deployUserRe)
	}
	if v.DeployPublicKey != "" && !deployKeyRe.MatchString(strings.TrimRight(v.DeployPublicKey, "\n")) {
		return fmt.Errorf(`ssh.deployPublicKey is not a single-line OpenSSH public key ("<type> <base64> [comment]", no quote in the comment)`)
	}
	return nil
}

// Assemble reads the template at absolutePath, substitutes the cloud-init
// placeholders, and appends the inforge-managed first-boot steps.
func Assemble(absolutePath string, vars Vars) (string, error) {
	template, err := os.ReadFile(absolutePath) // #nosec G304 -- absolutePath comes from the operator-authored, schema-validated resource manifest
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
