package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/types"
)

// meshStore builds an in-memory PKI store with a two-tier `wardnet-mesh` (with
// the given intermediate scopes) and a root-only `wardnet-daemon`.
func meshStore(scopes ...string) *pki.Store {
	inter := map[string]pki.Material{}
	for _, s := range scopes {
		inter[s] = pki.Material{Cert: "c", Key: "k"}
	}
	st := &pki.Store{RootRecipient: "age1root", Recipient: "age1ci"}
	st.Set("wardnet-mesh", pki.PKI{Topology: pki.TopologyTwoTier, Root: pki.Material{Cert: "c", Key: "k"}, Intermediates: inter})
	st.Set("wardnet-daemon", pki.PKI{Topology: pki.TopologyRootOnly, Scope: "global", Root: pki.Material{Cert: "c", Key: "k"}})
	return st
}

func svc(pkiRef string) types.ServiceSpec {
	return types.ServiceSpec{Name: "api", Container: "bridge", Host: "bridge", Type: "raw", User: "api", Pki: pkiRef}
}

func TestServicePKIErrors(t *testing.T) {
	full := meshStore(pki.ScopeGlobal, "us-east-1")

	t.Run("valid global service", func(t *testing.T) {
		assert.Empty(t, servicePKIErrors(svc("wardnet-mesh"), []string{pki.ScopeGlobal}, full))
	})
	t.Run("valid regional service", func(t *testing.T) {
		assert.Empty(t, servicePKIErrors(svc("wardnet-mesh"), []string{"us-east-1"}, full))
	})
	t.Run("empty pki is left to the schema pass", func(t *testing.T) {
		assert.Empty(t, servicePKIErrors(svc(""), []string{pki.ScopeGlobal}, full))
	})
	t.Run("no store", func(t *testing.T) {
		errs := servicePKIErrors(svc("wardnet-mesh"), []string{pki.ScopeGlobal}, nil)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0], "inforge pki init")
	})
	t.Run("unknown PKI", func(t *testing.T) {
		errs := servicePKIErrors(svc("nope"), []string{pki.ScopeGlobal}, full)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0], "unknown PKI")
	})
	t.Run("root-only PKI rejected as mesh", func(t *testing.T) {
		errs := servicePKIErrors(svc("wardnet-daemon"), []string{pki.ScopeGlobal}, full)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0], "must name a two-tier")
	})
	t.Run("missing intermediate for the service's scope", func(t *testing.T) {
		// mesh has only the global intermediate; a regional service needs us-east-1.
		errs := servicePKIErrors(svc("wardnet-mesh"), []string{"us-east-1"}, meshStore(pki.ScopeGlobal))
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0], `no intermediate for scope "us-east-1"`)
		assert.Contains(t, errs[0], "inforge pki intermediate")
	})
	t.Run("one error per missing scope", func(t *testing.T) {
		errs := servicePKIErrors(svc("wardnet-mesh"), []string{pki.ScopeGlobal, "us-east-1", "eu-central-1"}, meshStore(pki.ScopeGlobal))
		assert.Len(t, errs, 2) // us-east-1 and eu-central-1 missing; global present
	})
}
