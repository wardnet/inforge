package grant

import (
	"errors"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// PKIResource is the Grantable for a root-only PKI resource — the daemon's
// standalone CA. A ro (verify) grant delivers the CA cert only (trust peers); a
// rw (issue) grant additionally delivers the root signing key (mint certs
// online). Both fields are file fields (on-host PEM paths). The materialization —
// reading the age-encrypted sidecar and projecting the PEMs — lands in slice C of
// #117; this slice ships FieldNames so grant validation is real and testable.
type PKIResource struct{}

// FieldNames returns the file fields published for perm: CERT for verify (ro),
// CERT + KEY for issue (rw).
func (PKIResource) FieldNames(perm Permission) (values, files []string) {
	switch perm {
	case PermissionRO:
		return nil, []string{"CERT"}
	case PermissionRW:
		return nil, []string{"CERT", "KEY"}
	default:
		return nil, nil
	}
}

// Grant is deferred to slice C of #117 (read the resource's age-encrypted
// pki.enc.yaml sidecar; deliver the cert, and for issue the signing key, as file
// fields projected to the host).
func (PKIResource) Grant(_ *pulumi.Context, _ string, _ Permission, _, _ string) (Fields, error) {
	return Fields{}, errors.New("grant: PKIResource.Grant not yet implemented (slice C of #117)")
}
