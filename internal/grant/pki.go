package grant

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// FieldCert and FieldKey are the two file fields a root-only PKI resource
// publishes: the CA certificate (the trust anchor — verify) and the root signing
// key (issue). They are the {FIELD} placeholder names a grant's outputs: template
// composes over, and they name the on-host file each projects to.
const (
	FieldCert = "CERT"
	FieldKey  = "KEY"
)

// pkiDir is the descriptor/blob file-key namespace for PKI grant material. It
// mirrors meshcert.MtlsDir: a key doubles as the material's path relative to the
// service's tmpfs RuntimeDir, so PKI PEMs can never collide with the mesh
// material projected under "mtls/".
const pkiDir = "pki"

// FileKey is the single source of the file key a PKI grant's field projects to —
// the key that appears BOTH in the descriptor's files: map (env-var -> key) and
// in the host secrets blob's Files map (key -> PEM). The agent's projectFiles
// joins it onto the service's RuntimeDir, so it is equally the material's on-host
// relative path: "pki/<name>/cert.pem" or "pki/<name>/key.pem".
//
// Producer (the deploy) and consumer (the agent) must agree byte-for-byte, so
// both sides derive the key here — never hand-roll the string.
func FileKey(pkiName, field string) string {
	return pkiDir + "/" + pkiName + "/" + strings.ToLower(field) + ".pem"
}

// PKIResource is the Grantable for a root-only PKI resource — the daemon's
// standalone CA. A ro (verify) grant delivers the CA cert only (trust peers); a
// rw (issue) grant additionally delivers the root signing key (mint certs
// online). Both fields are file fields (on-host PEM paths).
//
// The zero value is the credential-free shape the validator resolves FieldNames
// on (see the grantable-fieldnames-must-be-credential-free rule). Grant needs the
// real material, which the caller decrypts once per deploy from the environment's
// pki.enc.yaml and injects here — the same construction Database uses for its
// RoleProvisioner. This package itself never reads the store nor touches an age
// identity.
type PKIResource struct {
	// Cert is the CA certificate PEM (plaintext in the store — it is public).
	Cert string
	// Key is the root signing key PEM, already decrypted. Empty is legal for a ro
	// grant, which never publishes KEY.
	Key string
}

// FieldNames returns the file fields published for perm: CERT for verify (ro),
// CERT + KEY for issue (rw).
func (PKIResource) FieldNames(perm Permission) (values, files []string) {
	switch perm {
	case PermissionRO:
		return nil, []string{FieldCert}
	case PermissionRW:
		return nil, []string{FieldCert, FieldKey}
	default:
		return nil, nil
	}
}

// Grant materializes the PKI credential for service under perm: it returns the
// file fields the resource publishes, each carrying the PEM the caller projects
// onto the host.
//
// It mints nothing. A root-only PKI's material is generated once by `inforge pki
// add --topology root-only` and lives in the committed store, so the grant is a
// pure delivery of already-existing material — unlike Database.Grant, which
// creates a role. It therefore registers no Pulumi resource and ignores
// ctx/env/region.
//
// It errors when the material perm requires is absent: a rw grant with no signing
// key would otherwise deliver a cert-only set, and the consumer would crash-loop
// at startup on the missing env var rather than fail the deploy.
func (p PKIResource) Grant(_ *pulumi.Context, service string, perm Permission, _, _ string) (Fields, error) {
	_, names := p.FieldNames(perm)
	if len(names) == 0 {
		return Fields{}, fmt.Errorf("service %q: unknown permission %q", service, perm)
	}
	material := map[string]string{FieldCert: p.Cert, FieldKey: p.Key}
	files := make(map[string]FileMaterial, len(names))
	for _, name := range names {
		pem := material[name]
		if pem == "" {
			return Fields{}, fmt.Errorf("service %q: the PKI resource publishes no %s material for permission %q", service, name, perm)
		}
		files[name] = FileMaterial{PEM: pulumi.String(pem).ToStringOutput()}
	}
	return Fields{Files: files}, nil
}
