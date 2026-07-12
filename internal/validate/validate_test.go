package validate

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/nginx"
	"github.com/wardnet/inforge/internal/pgrole"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/sizes"
	"github.com/wardnet/inforge/internal/types"
)

const testdataDir = "testdata"

// lineContaining returns the first line of out holding substr (empty if none),
// so a test can assert on the advice attached to ONE specific error rather than
// on the whole captured report.
func lineContaining(out, substr string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written. The reporter prints per-resource OK/FAIL lines to stdout, so a test can
// assert a specific validation message fired (the returned error is only the
// summary "validation failed").
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

func TestValidateResourcesOK(t *testing.T) {
	err := ValidateResources("ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "the ok environment should validate cleanly")
}

func TestValidateResourcesPkiMissingIntermediate(t *testing.T) {
	// The mesh PKI has only the global intermediate; the regional services need a
	// us-east-1 intermediate, so validation fails (the "silent miss" guard).
	err := ValidateResources("pki-missing-intermediate", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "a regional service whose scope has no mesh intermediate should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

func TestValidateResourcesBad(t *testing.T) {
	err := ValidateResources("bad", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "the bad environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesProviderDefaultsOK: specs that omit provider: pass when
// project-level defaults cover all resource classes.
func TestValidateResourcesProviderDefaultsOK(t *testing.T) {
	defaults := types.ProviderDefaults{
		Compute:  "hetzner",
		Database: map[string]string{"postgresql": "neon"},
	}
	err := ValidateResources("provider-defaults-ok", testdataDir, defaults)
	assert.NoError(t, err, "specs without provider: should pass when defaults cover all classes")
}

// TestValidateResourcesProviderDefaultsFail: specs that omit provider: fail when
// no defaults are configured.
func TestValidateResourcesProviderDefaultsFail(t *testing.T) {
	err := ValidateResources("provider-defaults-ok", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "specs without provider: should fail when no defaults are configured")
	assert.Contains(t, err.Error(), "validation failed")
}

func TestValidateResourcesNamingAlias(t *testing.T) {
	err := ValidateResources("naming-alias", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "the naming-alias environment should validate cleanly")
}

func TestValidateResourcesNamingAliasMulti(t *testing.T) {
	err := ValidateResources("naming-alias-multi", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "the naming-alias-multi environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesGlobalOK validates an environment whose regional secret
// resolves a GLOBAL database output (ref:database/global/shared.connectionUrl) —
// the one allowed cross-region reference.
func TestValidateResourcesGlobalOK(t *testing.T) {
	err := ValidateResources("global-ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "a regional secret referencing a global database should validate cleanly")
}

// TestValidateResourcesGlobalBad validates an environment whose GLOBAL secret
// references a REGIONAL database: the global slice is validated in a global-only
// context, so the regional database is not found and validation fails — enforcing
// "global → global only".
func TestValidateResourcesGlobalBad(t *testing.T) {
	err := ValidateResources("global-bad", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "a global resource referencing a regional one should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesGlobalDNSMissing: a global slice with a tls-termination
// service + ingress realizes DNS records and ACME certs against the placement
// region's authority. When that region (us-east-1) declares no dns: block, the
// records/certs would silently never deploy, so validation must reject the slice
// with a clear, actionable message rather than passing as it did before the fix.
func TestValidateResourcesGlobalDNSMissing(t *testing.T) {
	out := captureStdout(t, func() {
		err := ValidateResources("global-dns-missing", testdataDir, types.ProviderDefaults{})
		require.Error(t, err, "a global tls-termination service with no placement-region dns: must fail")
		assert.Contains(t, err.Error(), "validation failed")
	})
	assert.Contains(t, out, "placementRegion \"us-east-1\" has no dns: authority",
		"the failure must name the missing-dns guard, not some unrelated error")
}

// TestValidateResourcesGlobalPlacementUndefined: when placementRegion names an
// undefined region, the DNS guard must stay silent — indexing the region table with
// a bad key yields a zero-value (nil Dns) that would otherwise pile a misleading "no
// dns: authority" error on top of the real "not a defined region" error. The
// operator should see only the latter.
func TestValidateResourcesGlobalPlacementUndefined(t *testing.T) {
	out := captureStdout(t, func() {
		err := ValidateResources("global-placement-undefined", testdataDir, types.ProviderDefaults{})
		require.Error(t, err, "an undefined placementRegion must fail validation")
	})
	assert.Contains(t, out, "is not a defined region", "the real error must be reported")
	assert.NotContains(t, out, "has no dns: authority",
		"the DNS guard must not fire for a region that does not exist")
}

// TestValidateResourcesEncryptedOK: a `vault:KEY` secret whose (service, KEY)
// ciphertext exists in the env's secrets.enc.yaml validates
// cleanly — the check is presence-only, so the fixture ciphertext is a dummy.
func TestValidateResourcesEncryptedOK(t *testing.T) {
	err := ValidateResources("encrypted-ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "an encrypted source with a matching store entry should validate cleanly")
}

// TestCheckSecretStoreEntries covers the store→manifest direction (ADR-0040): an
// entry nothing reads is an error, because it is the one mismatch that would
// otherwise stay silent until a service came up without its value. Both orphan
// shapes appear in a hand migration off the container-keyed store: an entry left
// under the old container name (no such service), and a key moved to the wrong
// service (that service declares no such `vault:` ref).
func TestCheckSecretStoreEntries(t *testing.T) {
	// api and web share a container; each declares its own vault key.
	res := types.Resources{Service: []types.ServiceSpec{
		{Name: "api", Container: "bridge", Environment: map[string]string{"T": "vault:API_TOKEN"}},
		{Name: "web", Container: "bridge", Environment: map[string]string{"T": "vault:WEB_TOKEN"}},
	}}
	global := types.Resources{Service: []types.ServiceSpec{
		{Name: "edge", Container: "edge", Environment: map[string]string{"T": "vault:EDGE_TOKEN"}},
	}}

	store := &secretstore.Store{Recipient: "age1test"}
	store.Set("api", "API_TOKEN", "ct")
	store.Set("web", "WEB_TOKEN", "ct")
	store.Set("edge", "EDGE_TOKEN", "ct") // the global slice counts too
	store.SetReserved("observability", "anything_at_all", "ct")

	st := secretStoreState{store: store}
	clean := &reporter{}
	captureStdout(t, func() { checkSecretStoreEntries(clean, st, res, global, "secrets.enc.yaml") })
	assert.False(t, clean.failed, "every entry is declared by its service — and a reserved key is never checked")

	// An entry left under the old CONTAINER name: no service is called "bridge".
	store.Set("bridge", "API_TOKEN", "ct")
	// A key moved to the wrong service: web declares no `vault:API_TOKEN`.
	store.Set("web", "API_TOKEN", "ct")

	dirty := &reporter{}
	out := captureStdout(t, func() { checkSecretStoreEntries(dirty, st, res, global, "secrets.enc.yaml") })
	assert.True(t, dirty.failed)
	assert.Contains(t, out, `no service named "bridge" is declared`)
	assert.Contains(t, out, "services.web.API_TOKEN")
	assert.NotContains(t, out, "services.api.API_TOKEN", "a correctly-placed entry must not be flagged")

	// The advice must be RUNNABLE. `inforge secret rm` calls requireService, so it
	// refuses an undeclared service — offering it for the "bridge" orphan would
	// send the operator to a command that exits 1. That entry is a hand edit; only
	// the "web" orphan (a real service) may be pointed at `secret rm`.
	bridgeLine := lineContaining(out, `no service named "bridge"`)
	assert.NotContains(t, bridgeLine, "inforge secret rm", "the CLI cannot remove an entry for an undeclared service")
	assert.Contains(t, bridgeLine, "by hand")
	assert.Contains(t, lineContaining(out, "services.web.API_TOKEN"), "inforge secret rm",
		"web IS declared, so the CLI can remove its orphaned key")
}

// TestSecretStoreStateUnreadableIsNotAbsent: a store that EXISTS but cannot be
// read (corrupt, or still carrying a pre-ADR-0040 `containers:` block) must not
// be reported as absent. The store-level error is printed once against its own
// path; telling the operator per `vault:` ref that the file "does not exist —
// run `inforge secret init`" would be false, would bury the one real error, and
// names the single command that must NOT be run against a live store.
func TestSecretStoreStateUnreadableIsNotAbsent(t *testing.T) {
	// Absent: no conclusion is possible, but the "not initialized" advice is right.
	_, known := secretStoreState{}.lookup("api", "K")
	assert.False(t, known, "an absent store cannot answer a lookup")

	// Unreadable: also unanswerable — and the caller must stay silent, not fall
	// through to the absent-store advice.
	unreadable := secretStoreState{unreadable: true}
	_, known = unreadable.lookup("api", "K")
	assert.False(t, known)
	assert.True(t, unreadable.unreadable, "the caller distinguishes this from absent and says nothing")

	// Loaded: answers normally.
	store := &secretstore.Store{Recipient: "age1test"}
	store.Set("api", "K", "ct")
	found, known := secretStoreState{store: store}.lookup("api", "K")
	assert.True(t, known)
	assert.True(t, found)
	found, known = secretStoreState{store: store}.lookup("api", "MISSING")
	assert.True(t, known)
	assert.False(t, found, "a loaded store reports a genuinely missing key")
}

// TestValidateResourcesEncryptedMissingKey: the store exists but holds no
// ciphertext for the service's `vault:KEY`.
func TestValidateResourcesEncryptedMissingKey(t *testing.T) {
	err := ValidateResources("encrypted-bad", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an encrypted source without a store entry should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesEncryptedNoStore: a `vault:KEY` secret with no
// secrets.enc.yaml must fail and point at `inforge secret init`.
func TestValidateResourcesEncryptedNoStore(t *testing.T) {
	err := ValidateResources("encrypted-nostore", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an encrypted source without a store file should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesIngressAppOK: an ingress + app at both regional and global
// scope, each ingress fronting a same-scope compute host and each app resolving
// its ingress within its own scope, validates cleanly.
func TestValidateResourcesIngressAppOK(t *testing.T) {
	err := ValidateResources("ingress-app-ok", testdataDir, types.ProviderDefaults{})
	assert.NoError(t, err, "regional and global ingress/app referencing same-scope hosts should validate cleanly")
}

// TestValidateResourcesForwardOn80WithApp: an ingress host that terminates TLS
// only because it serves an APP (no tls-termination route anywhere) still owns
// :80 — nginx renders the ACME HTTP-01/redirect server there. A forward on :80
// on that ingress is a stream bind on the same socket, so nginx would refuse to
// start; validation must reject it.
func TestValidateResourcesForwardOn80WithApp(t *testing.T) {
	var err error
	out := captureStdout(t, func() {
		err = ValidateResources("forward-80-app", testdataDir, types.ProviderDefaults{})
	})
	require.Error(t, err, "a forward on :80 on a host serving an app should fail validation")
	assert.Contains(t, out, "ACME owns :80")
}

// TestValidateResourcesAppBadIngress: an app whose ingress: foreign key names no
// ingress resource in its scope fails validation (the same-scope FK rule).
func TestValidateResourcesAppBadIngress(t *testing.T) {
	err := ValidateResources("app-bad-ingress", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an app referencing an unknown ingress should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesIngressBadHost: an ingress whose host: foreign key names no
// compute in its scope fails validation (the same-scope host FK rule).
func TestValidateResourcesIngressBadHost(t *testing.T) {
	err := ValidateResources("ingress-bad-host", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "an ingress referencing an unknown compute host should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesAppDuplicateSubdomain: two apps in one scope sharing a
// subdomain fail — each app must map to a distinct public FQDN.
func TestValidateResourcesAppDuplicateSubdomain(t *testing.T) {
	err := ValidateResources("app-duplicate-subdomain", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two apps sharing a subdomain in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesAppDuplicateName: two apps in one scope sharing a name fail.
// Resource-name uniqueness is enforced generically in validateType (a duplicate
// would silently overwrite the scope's FK-resolution map), so it applies to every
// resource type — see the compute/ingress duplicate-name cases below.
func TestValidateResourcesAppDuplicateName(t *testing.T) {
	err := ValidateResources("app-duplicate-name", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two apps sharing a name in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesComputeDuplicateName: two compute folders declaring the same
// `name:` fail — name uniqueness is generic, not an app/ingress special case.
func TestValidateResourcesComputeDuplicateName(t *testing.T) {
	err := ValidateResources("compute-duplicate-name", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two computes sharing a name in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestValidateResourcesIngressDuplicateName: two ingress folders declaring the same
// `name:` fail (same generic uniqueness path).
func TestValidateResourcesIngressDuplicateName(t *testing.T) {
	err := ValidateResources("ingress-duplicate-name", testdataDir, types.ProviderDefaults{})
	require.Error(t, err, "two ingresses sharing a name in one scope should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}

// TestCheckCrossScopeNamesRejectsServiceCollision: a service name declared in
// both the regional and global scopes is rejected — deployDescriptor merges
// both scopes by bare name (no scope discriminator in resolveDeployTargets),
// so a collision would deploy to both targets from one `--service <name>`.
func TestCheckCrossScopeNamesRejectsServiceCollision(t *testing.T) {
	regional := types.Resources{Service: []types.ServiceSpec{{Name: "api"}}}
	global := types.Resources{Service: []types.ServiceSpec{{Name: "api"}}}

	r := &reporter{}
	out := captureStdout(t, func() {
		checkCrossScopeNames(r, regional, global, "regional", "global")
	})
	assert.True(t, r.failed)
	assert.Contains(t, out, `service name "api" is declared in both the global scope and the regional scope`)
}

// TestCheckCrossScopeNamesRejectsAppCollision mirrors the service case for apps.
func TestCheckCrossScopeNamesRejectsAppCollision(t *testing.T) {
	regional := types.Resources{App: []types.AppSpec{{Name: "dashboard"}}}
	global := types.Resources{App: []types.AppSpec{{Name: "dashboard"}}}

	r := &reporter{}
	out := captureStdout(t, func() {
		checkCrossScopeNames(r, regional, global, "regional", "global")
	})
	assert.True(t, r.failed)
	assert.Contains(t, out, `app name "dashboard" is declared in both the global scope and the regional scope`)
}

// TestCheckCrossScopeNamesAllowsDistinctNames: distinct names in each scope
// (the common case) never trip the cross-scope check.
func TestCheckCrossScopeNamesAllowsDistinctNames(t *testing.T) {
	regional := types.Resources{
		Service: []types.ServiceSpec{{Name: "api"}},
		App:     []types.AppSpec{{Name: "dashboard"}},
	}
	global := types.Resources{
		Service: []types.ServiceSpec{{Name: "tenants"}},
		App:     []types.AppSpec{{Name: "admin"}},
	}

	r := &reporter{}
	captureStdout(t, func() {
		checkCrossScopeNames(r, regional, global, "regional", "global")
	})
	assert.False(t, r.failed)
}

// TestCheckNetworkSubnetNamesUniqueAcrossNetworks: a subnet's provider resource
// name derives from the subnet name alone, so two networks of one scope reusing
// a subnet name collide on one URN at deploy. Validation rejects it — including
// the same name declared twice by one network — while distinct names pass.
func TestCheckNetworkSubnetNamesUniqueAcrossNetworks(t *testing.T) {
	ctx := baseCtx()
	corenet := types.NetworkSpec{
		Name: "corenet", Container: "core", Provider: "hetzner", CIDR: "10.0.0.0/16",
		Subnets: []types.SubnetSpec{{Name: "app", CIDR: "10.0.1.0/24"}},
	}
	edgenet := types.NetworkSpec{
		Name: "edgenet", Container: "edge", Provider: "hetzner", CIDR: "10.1.0.0/16",
		Subnets: []types.SubnetSpec{{Name: "app", CIDR: "10.1.1.0/24"}},
	}
	ctx.networks = map[string]types.NetworkSpec{"corenet": corenet, "edgenet": edgenet}

	errs, _ := checkNetwork(edgenet, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), `"app" is also declared by network "corenet"`)

	// The collision is reported ONCE per colliding subnet, even when the other
	// network declares the name more than once.
	corenet.Subnets = append(corenet.Subnets, types.SubnetSpec{Name: "app", CIDR: "10.0.2.0/24"})
	ctx.networks["corenet"] = corenet
	errs, _ = checkNetwork(edgenet, ctx)
	assert.Len(t, errs, 1)
	corenet.Subnets = corenet.Subnets[:1]
	ctx.networks["corenet"] = corenet

	// A distinct name in the second network passes.
	edgenet.Subnets = []types.SubnetSpec{{Name: "edge-app", CIDR: "10.1.1.0/24"}}
	ctx.networks["edgenet"] = edgenet
	errs, _ = checkNetwork(edgenet, ctx)
	assert.Empty(t, errs)

	// A name repeated within one network is rejected too.
	edgenet.Subnets = append(edgenet.Subnets, types.SubnetSpec{Name: "edge-app", CIDR: "10.1.2.0/24"})
	ctx.networks["edgenet"] = edgenet
	errs, _ = checkNetwork(edgenet, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "declared twice by this network")
}

// TestCheckNetworkContainerCIDRAgreement: a container is realized as ONE cloud
// network, with the CIDR of the first spec that reached it — so two networks
// sharing a container must agree on the CIDR, or the later spec's subnets land
// out of range.
func TestCheckNetworkContainerCIDRAgreement(t *testing.T) {
	ctx := baseCtx()
	first := types.NetworkSpec{Name: "first", Container: "edge", Provider: "hetzner", CIDR: "10.0.0.0/16"}
	second := types.NetworkSpec{Name: "second", Container: "edge", Provider: "hetzner", CIDR: "10.1.0.0/16"}
	ctx.networks = map[string]types.NetworkSpec{"first": first, "second": second}

	errs, _ := checkNetwork(second, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), `both share container "edge"`)

	// Agreeing CIDRs pass.
	second.CIDR = "10.0.0.0/16"
	ctx.networks["second"] = second
	errs, _ = checkNetwork(second, ctx)
	assert.Empty(t, errs)
}

// TestCheckComputeGlobalNetworkRejected: a compute attaching to a global network
// is recognized but rejected (cross-region networking not supported yet).
func TestCheckComputeGlobalNetworkRejected(t *testing.T) {
	ctx := baseCtx()
	ctx.sizeTable = sizes.DefaultTable()
	ctx.networks = map[string]types.NetworkSpec{}
	errs, _ := checkCompute(types.ComputeSpec{
		Provider: "hetzner", Network: "global/corenet", Size: "SMALL", Kind: "vm",
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "not supported yet")
	// The generic "network not found" message must NOT also fire for a global ref.
	for _, e := range errs {
		assert.NotContains(t, e, "not found")
	}
}

// TestCheckComputeSubnetFK: an optional subnet: FK must resolve to a subnet of
// the compute's own network. A valid one passes; an unknown one is rejected at
// validate time rather than only failing at deploy (resolveNetworkOutput).
func TestCheckComputeSubnetFK(t *testing.T) {
	ctx := baseCtx()
	ctx.sizeTable = sizes.DefaultTable()
	ctx.networks = map[string]types.NetworkSpec{
		"corenet": {Name: "corenet", Subnets: []types.SubnetSpec{{Name: "app"}, {Name: "data"}}},
	}
	base := types.ComputeSpec{Provider: "hetzner", Network: "corenet", Size: "SMALL", Kind: "vm"}

	// A declared subnet resolves.
	ok := base
	ok.Subnet = "data"
	errs, _ := checkCompute(ok, ctx)
	assert.Empty(t, errs)

	// An unknown subnet is rejected.
	bad := base
	bad.Subnet = "nope"
	errs, _ = checkCompute(bad, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), `subnet: "nope" is not a subnet of network "corenet"`)

	// No subnet is fine (defaults to the first-declared subnet).
	errs, _ = checkCompute(base, ctx)
	assert.Empty(t, errs)
}

// TestCheckServiceGlobalHostRejected: a service on a global host is rejected —
// such a service is defined in the global slice itself, not referenced from a region.
func TestCheckServiceGlobalHostRejected(t *testing.T) {
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{
		Host: "global/edge-01", Type: "raw", User: "svc",
	}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "defined in the global slice itself")
}

// ingressCtx returns a baseCtx seeded with one ingress "web" (fronting the bridge
// host) so app FK checks resolve, plus an empty app-subdomain count map. (Bare-name
// uniqueness is enforced generically in validateType, not in checkIngress/checkApp.)
func ingressCtx() regionContext {
	ctx := baseCtx()
	ctx.ingressNames = map[string]bool{"web": true}
	ctx.appSubdomainCounts = map[string]int{}
	return ctx
}

// TestCheckIngressValid: an ingress whose host: resolves to a same-scope
// single-instance vm compute passes.
func TestCheckIngressValid(t *testing.T) {
	errs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "bridge"}, ingressCtx())
	assert.Empty(t, errs)
}

// TestCheckIngressGlobalHostRejected: an ingress referencing a global compute is
// rejected — like service.host, it is declared in the global slice itself.
func TestCheckIngressGlobalHostRejected(t *testing.T) {
	errs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "global/edge"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "global slice itself")
}

// TestCheckIngressUnknownHostRejected: an ingress whose host: does not resolve to
// a compute in the same scope fails the foreign-key check.
func TestCheckIngressUnknownHostRejected(t *testing.T) {
	errs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "ghost"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not resolve to a compute")
}

// TestCheckIngressHealthPortCollidesWithTLSBind: an app (or the scope gateway) on
// the ingress host binds :443 without a route listen entry, so the public health
// port must not be 443 — the health servers are plain HTTP and their port carries
// the listener's default_server, so the collision would both double-bind the socket
// and hand :443's default server to a plain-HTTP 404.
func TestCheckIngressHealthPortCollidesWithTLSBind(t *testing.T) {
	ctx := ingressCtx()
	ctx.nginxBindsByHost = map[string]map[int]string{"bridge-01": {443: "app TLS listener"}}
	errs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "bridge", HealthProbesPort: 443}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "already bound on this ingress host by nginx (app TLS listener)")

	// The health listener's OWN registered bind is not a collision with itself.
	ctx.nginxBindsByHost = map[string]map[int]string{"bridge-01": {80: "ACME HTTP-01 challenge/redirect", 81: bindReasonHealth}}
	errs, _ = checkIngress(types.IngressSpec{Name: "web", Host: "bridge", HealthProbesPort: 81}, ctx)
	assert.Empty(t, errs)
}

// TestCheckAppGlobalIngressRejected: an app referencing a global ingress is
// rejected — like service.host, an app served from a global ingress is declared
// in the global slice itself, not referenced from a region.
func TestCheckAppGlobalIngressRejected(t *testing.T) {
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "global/web", Subdomain: "my"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "global slice itself")
}

// TestCheckAppUnknownIngressRejected: an app whose ingress: does not resolve to an
// ingress in the same scope fails the foreign-key check.
func TestCheckAppUnknownIngressRejected(t *testing.T) {
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "ghost", Subdomain: "my"}, ingressCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not resolve to an ingress resource")
}

// TestCheckAppValid: an app whose ingress: resolves to a same-scope ingress passes.
func TestCheckAppValid(t *testing.T) {
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "web", Subdomain: "my", Spa: true}, ingressCtx())
	assert.Empty(t, errs)
}

// TestCheckAppDuplicateSubdomain: two apps sharing a subdomain in one scope are
// rejected (each app must map to a distinct public FQDN).
func TestCheckAppDuplicateSubdomain(t *testing.T) {
	ctx := ingressCtx()
	ctx.appSubdomainCounts["my"] = 2
	errs, _ := checkApp(types.AppSpec{Name: "dash", Ingress: "web", Subdomain: "my"}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[0], "distinct public FQDN")
}

// TestCheckServiceDatabaseRefRejected: a ref:database/* is rejected — a database
// exposes no referenceable outputs (ADR-0025); DB credentials flow only through a
// grant. Regional access to a global database is exercised via grants (a
// database/global/<name> grant) in grant_test.go.
func TestCheckServiceDatabaseRefRejected(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil // provider availability is checked separately, per region
	ctx.databaseNames = map[string]bool{"global/shared": true}
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"DB": "ref:database/global/shared.connectionUrl"},
	}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "use a grants: entry")
}

// TestCheckSecretsRejectsReservedEnvName: a secret key (which becomes the
// service's env var name) must not claim the reserved INFORGE_* namespace.
func TestCheckSecretsRejectsReservedEnvName(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"INFORGE_DEPLOYMENT_REGION": "env:SOME_SECRET"},
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved")
}

// TestCheckServiceRejectsReservedMeshEnvName: an env var name colliding with a
// reserved mesh certificate path (MTLS_*_PATH) must fail — the host projection
// would otherwise overwrite the user's value with the leaf path.
func TestCheckServiceRejectsReservedMeshEnvName(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Environment: map[string]string{"MTLS_LEAF_CERT_PATH": "env:SOME_SECRET"},
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved")
}

// TestCheckServiceRejectsMultilineReload: reload becomes a single ExecReload=
// line in the unit, so a newline would inject extra directives.
func TestCheckServiceRejectsMultilineReload(t *testing.T) {
	ctx := baseCtx()
	ctx.available = nil
	errs, _ := checkService(types.ServiceSpec{
		Host: "bridge", Type: "raw", User: "svc", Container: "ghost",
		Reload: "nginx -s reload\nExecStart=/bin/evil",
	}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "single line")
}

// TestBuildGlobalRefs derives referenceable database names and compute keys from
// the global resource set, keying single-instance computes by both forms.
func TestBuildGlobalRefs(t *testing.T) {
	g := buildGlobalRefs(types.Resources{
		Database: []types.DatabaseSpec{{Name: "shared"}},
		Compute:  []types.ComputeSpec{{Name: "edge", Kind: "vm", InstanceCount: 1}},
	})
	assert.True(t, g.databaseNames["shared"])
	assert.Equal(t, "vm", g.computeKind["edge"])
	assert.Equal(t, "vm", g.computeKind["edge-01"])
}

// TestCheckRegionsFile exercises the regions.yaml validation paths directly: the
// ok/bad fixtures only cover the happy path and base_domain, so the per-region
// slug/providers and global checks need explicit coverage.
func TestCheckRegionsFile(t *testing.T) {
	withProviders := map[string]map[string]any{"hetzner": {}}

	t.Run("valid", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, nil, "regions.yaml")
		assert.False(t, r.failed)
	})

	t.Run("empty table", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("missing slug", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Providers: withProviders},
		}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("empty providers block", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1"},
		}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global without providers", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{PlacementRegion: "us-east-1"}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global missing placementRegion", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{Providers: withProviders}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global unknown placementRegion", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{PlacementRegion: "eu-west-1", Providers: withProviders}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global with providers and placementRegion", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders},
		}, &regions.Global{PlacementRegion: "us-east-1", Providers: withProviders}, "regions.yaml")
		assert.False(t, r.failed)
	})

	t.Run("dns authority with provider and zone", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders,
				Dns: &regions.DnsAuthority{Provider: "cloudflare", Zone: "z1"}},
		}, nil, "regions.yaml")
		assert.False(t, r.failed)
	})

	t.Run("dns authority missing zone fails", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: withProviders,
				Dns: &regions.DnsAuthority{Provider: "cloudflare"}},
		}, nil, "regions.yaml")
		assert.True(t, r.failed)
	})

	// The global slice's DNS authority is the placement region's, but its providers
	// come from the global block: the credentials must be in BOTH, or the global DNS
	// provider is registered with an empty token and every global record fails at apply.
	t.Run("global providers missing the dns authority's provider", func(t *testing.T) {
		r := &reporter{}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: map[string]map[string]any{
				"hetzner":    {"apiToken": "t"},
				"cloudflare": {"apiToken": "t"},
			}, Dns: &regions.DnsAuthority{Provider: "cloudflare", Zone: "z1"}},
		}, &regions.Global{PlacementRegion: "us-east-1", Providers: map[string]map[string]any{
			"hetzner": {"apiToken": "t"},
		}}, "regions.yaml")
		assert.True(t, r.failed)
	})

	t.Run("global providers carry the dns authority's provider", func(t *testing.T) {
		r := &reporter{}
		full := map[string]map[string]any{
			"hetzner":    {"apiToken": "t"},
			"cloudflare": {"apiToken": "t"},
		}
		checkRegionsFile(r, regions.Table{
			"us-east-1": {Slug: "use1", Providers: full,
				Dns: &regions.DnsAuthority{Provider: "cloudflare", Zone: "z1"}},
		}, &regions.Global{PlacementRegion: "us-east-1", Providers: full}, "regions.yaml")
		assert.False(t, r.failed)
	})
}

// TestCheckProviderAvailabilityPerRegion confirms the single shared resource set
// must have each declared provider available in EVERY region it deploys into: a
// provider present in one region but absent from another fails for the region
// that lacks it. It runs against the ok fixture (which uses hetzner, cloudflare,
// and neon).
func TestCheckProviderAvailabilityPerRegion(t *testing.T) {
	full := map[string]map[string]any{
		"hetzner": {}, "cloudflare": {}, "neon": {},
	}

	t.Run("all providers in every region", func(t *testing.T) {
		r := &reporter{}
		table := regions.Table{
			"us-east-1":    {Slug: "use1", Providers: full},
			"eu-central-1": {Slug: "euc1", Providers: full},
		}
		require.NoError(t, checkProviderAvailability(r, filepath.Join(testdataDir, "ok", "regional"), table, types.ProviderDefaults{}))
		assert.False(t, r.failed, "every region declares every provider the shared set uses")
	})

	t.Run("provider missing in one region", func(t *testing.T) {
		r := &reporter{}
		// eu-central-1 omits neon, which the shared database resource requires.
		noNeon := map[string]map[string]any{
			"hetzner": {}, "cloudflare": {},
		}
		table := regions.Table{
			"us-east-1":    {Slug: "use1", Providers: full},
			"eu-central-1": {Slug: "euc1", Providers: noNeon},
		}
		require.NoError(t, checkProviderAvailability(r, filepath.Join(testdataDir, "ok", "regional"), table, types.ProviderDefaults{}))
		assert.True(t, r.failed, "neon is unavailable in eu-central-1")
	})
}

// baseCtx returns a regionContext with one vm host (bridge) and hetzner
// available, for exercising the per-spec semantic checks directly.
func baseCtx() regionContext {
	return regionContext{
		available:        map[string]bool{"hetzner": true},
		computeNames:     map[string]bool{"bridge": true},
		computeKind:      map[string]string{"bridge-01": "vm"},
		computeCanonical: map[string]string{"bridge-01": "bridge-01", "bridge": "bridge-01"},
		computeDeployer:  map[string]bool{"bridge-01": true},
	}
}

// meshCtx seeds a baseCtx with a single-scope gateway and two mesh services (ddns,
// tunneller) that permit the gateway, plus a non-mesh service (nopki), so gateway
// routes and mesh allow-lists resolve (ADR-0032).
func meshCtx() regionContext {
	c := baseCtx()
	c.gatewayScopeCount = 1
	c.meshServices = map[string]bool{"ddns": true, "tunneller": true}
	c.serviceNamesInScope = map[string]bool{"ddns": true, "tunneller": true, "nopki": true}
	c.serviceAllowsGateway = map[string]bool{"ddns": true, "tunneller": true}
	c.servicePkiByName = map[string]string{"ddns": "mesh", "tunneller": "mesh"}
	c.servicePublicPathsByName = map[string][]string{"ddns": {"/dns/**"}, "tunneller": {"/tunnel/**"}}
	c.targetUsersByHost = map[string]map[int][]string{}
	c.portUsersByHost = map[string]map[int][]string{}
	return c
}

func meshSvc(m *types.MeshSpec) types.ServiceSpec {
	// The endpoint surface is allowlist-only (ADR-0034); default a path so tests
	// exercising other mesh rules don't trip the closed-by-default error.
	if m != nil && len(m.PublicPaths)+len(m.InternalPaths) == 0 {
		m.InternalPaths = []string{"/internal/**"}
	}
	return types.ServiceSpec{Name: "tenants", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh", Mesh: m}
}

func gw(services ...string) types.GatewaySpec {
	return types.GatewaySpec{Name: "api", Host: "bridge", Pki: "mesh", Subdomain: "api", Services: services}
}

// TestCheckGatewayValid: a gateway on a same-scope vm, sole in scope, listing a
// service that permits the gateway and publishes public paths, passes.
func TestCheckGatewayValid(t *testing.T) {
	errs, _ := checkGateway(gw("ddns"), meshCtx())
	assert.Empty(t, errs)
}

// TestCheckGatewayGlobalHostRejected: a gateway referencing a global compute is
// rejected — like service.host, it is declared in the global slice itself.
func TestCheckGatewayGlobalHostRejected(t *testing.T) {
	c := meshCtx()
	errs, _ := checkGateway(types.GatewaySpec{Name: "api", Host: "global/edge", Subdomain: "api"}, c)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "global slice itself")
}

// TestCheckGatewayUnknownHostRejected: a gateway whose host: does not resolve fails.
func TestCheckGatewayUnknownHostRejected(t *testing.T) {
	c := meshCtx()
	errs, _ := checkGateway(types.GatewaySpec{Name: "api", Host: "ghost", Subdomain: "api"}, c)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "does not resolve to a compute")
}

// TestCheckGatewaySingletonRejected: two gateways in one scope are rejected.
func TestCheckGatewaySingletonRejected(t *testing.T) {
	c := meshCtx()
	c.gatewayScopeCount = 2
	errs, _ := checkGateway(gw(), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "at most one gateway")
}

// TestCheckGatewayUnknownService: a listed service not in scope is rejected.
func TestCheckGatewayUnknownService(t *testing.T) {
	errs, _ := checkGateway(gw("ghost"), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not resolve to a service in this scope")
}

// TestCheckGatewayServiceDisallows: a listed service that does not permit the
// gateway (no "gateway" in its mesh.allowed_services) is rejected.
func TestCheckGatewayServiceDisallows(t *testing.T) {
	errs, _ := checkGateway(gw("nopki"), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not permit the gateway")
}

// TestCheckGatewayDuplicateService: a service listed twice is rejected.
func TestCheckGatewayDuplicateService(t *testing.T) {
	errs, _ := checkGateway(gw("ddns", "ddns"), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "listed more than once")
}

// TestCheckGatewayNoPublicPaths: a listed service with no mesh.public_paths would
// give the edge nothing to route — closed by default (ADR-0034).
func TestCheckGatewayNoPublicPaths(t *testing.T) {
	c := meshCtx()
	delete(c.servicePublicPathsByName, "ddns")
	errs, _ := checkGateway(gw("ddns"), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "declares no mesh.public_paths")
}

// TestCheckGatewayOverlappingPublicPaths: two listed services claiming overlapping
// public globs are rejected — a request path must have exactly one owner.
func TestCheckGatewayOverlappingPublicPaths(t *testing.T) {
	c := meshCtx()
	c.servicePublicPathsByName["ddns"] = []string{"/v*/dns/**"}
	c.servicePublicPathsByName["tunneller"] = []string{"/v1/**"}
	errs, _ := checkGateway(gw("ddns", "tunneller"), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "overlaps")
}

// TestCheckGatewayHealthPathShadowed: a gateway health probe path claimed by a
// listed service's public glob is rejected (the exact-match liveness location
// would shadow the service's endpoint).
func TestCheckGatewayHealthPathShadowed(t *testing.T) {
	g := gw("ddns")
	g.HealthProbePaths = []string{"/dns/healthz"}
	errs, _ := checkGateway(g, meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "would shadow")
}

// TestCheckGatewayHealthPathOK: a health path outside every public glob passes.
func TestCheckGatewayHealthPathOK(t *testing.T) {
	g := gw("ddns")
	g.HealthProbePaths = []string{"/healthz"}
	errs, _ := checkGateway(g, meshCtx())
	assert.Empty(t, errs)
}

// TestCheckGatewayHealthPortRules: the gateway's public health port must not be
// 80 (ACME), 443 (daemon TLS), and must match a co-hosted ingress's health port.
func TestCheckGatewayHealthPortRules(t *testing.T) {
	g := gw("ddns")
	g.HealthProbesPort = 80
	errs, _ := checkGateway(g, meshCtx())
	assert.Contains(t, strings.Join(errs, "\n"), "must not be 80")

	g.HealthProbesPort = 443
	errs, _ = checkGateway(g, meshCtx())
	assert.Contains(t, strings.Join(errs, "\n"), "must not be 443")

	c := meshCtx()
	c.ingressNamesByHost = map[string][]string{"bridge-01": {"web"}}
	c.ingressHealthPort = map[string]int{"web": 82}
	g.HealthProbesPort = 81
	errs, _ = checkGateway(g, c)
	assert.Contains(t, strings.Join(errs, "\n"), "one host renders one health listener")
}

// TestCheckGatewayPkiMismatch: a listed service in a DIFFERENT mesh than the
// gateway is rejected — the callee would never trust the gateway's client leaf.
func TestCheckGatewayPkiMismatch(t *testing.T) {
	c := meshCtx()
	c.servicePkiByName["ddns"] = "other-mesh"
	errs, _ := checkGateway(gw("ddns"), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must share one pki")
}

// TestCheckGatewaySubdomainCollidesWithApp: a gateway subdomain an app already
// claims in the scope is rejected (same flat FQDN namespace: DNS record + ACME).
func TestCheckGatewaySubdomainCollidesWithApp(t *testing.T) {
	c := meshCtx()
	c.appSubdomainCounts = map[string]int{"api": 1}
	errs, _ := checkGateway(gw(), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "already used by an app")
}

// TestCheckServiceGatewayNameReserved: a service named "gateway" would mint the
// gateway's mesh identity (CN=<scope>/gateway) and forge daemon-originated
// traffic — the name is reserved.
func TestCheckServiceGatewayNameReserved(t *testing.T) {
	c := meshCtx()
	s := types.ServiceSpec{Name: "gateway", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh"}
	errs, _ := checkService(s, c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved for the north-south gateway")
}

// TestCheckServiceMeshNameReserved: a service named "mesh" shares the per-host mesh
// proxy's runtime dir + systemd unit; stopping it would wipe the proxy's projected
// leaf/key/trust-bundle — the name is reserved.
func TestCheckServiceMeshNameReserved(t *testing.T) {
	c := meshCtx()
	s := types.ServiceSpec{Name: "mesh", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh"}
	errs, _ := checkService(s, c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved for the per-host mesh proxy")
}

// twoNetworkMeshCtx adds a second host "edge" on a DIFFERENT network to meshCtx,
// with the gateway on "edge" and its target ddns on "bridge", so the mesh dial
// edge would cross networks.
func twoNetworkMeshCtx() regionContext {
	c := meshCtx()
	c.computeNames["edge"] = true
	c.computeKind["edge-01"] = "vm"
	c.computeCanonical["edge-01"] = "edge-01"
	c.computeCanonical["edge"] = "edge-01"
	c.computeDeployer["edge-01"] = true
	c.computeNetwork = map[string]string{
		"bridge-01": "net-a", "bridge": "net-a",
		"edge-01": "net-b", "edge": "net-b",
	}
	c.serviceHostByName = map[string]string{"ddns": "bridge", "tunneller": "bridge"}
	return c
}

// TestCheckGatewayCrossNetworkTargetRejected: a gateway whose route target lives
// on a host in a different network is rejected — the mesh dials the target's
// private IP, unroutable across networks.
func TestCheckGatewayCrossNetworkTargetRejected(t *testing.T) {
	c := twoNetworkMeshCtx()
	g := types.GatewaySpec{Name: "api", Host: "edge", Pki: "mesh", Subdomain: "api",
		Services: []string{"ddns"}}
	errs, _ := checkGateway(g, c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must share a network")
}

// TestCheckGatewaySameNetworkTargetOK: same route with both hosts on one network passes.
func TestCheckGatewaySameNetworkTargetOK(t *testing.T) {
	c := twoNetworkMeshCtx()
	c.computeNetwork["edge-01"] = "net-a"
	c.computeNetwork["edge"] = "net-a"
	g := types.GatewaySpec{Name: "api", Host: "edge", Pki: "mesh", Subdomain: "api",
		Services: []string{"ddns"}}
	errs, _ := checkGateway(g, c)
	assert.Empty(t, errs)
}

// TestCheckMeshCrossNetworkCallerRejected: a same-scope caller on a different
// network than this callee is rejected — the mesh dials the callee's private IP.
func TestCheckMeshCrossNetworkCallerRejected(t *testing.T) {
	c := twoNetworkMeshCtx()
	// tunneller (the callee) is on bridge/net-a; ddns (a permitted caller) is
	// also on bridge — move ddns to edge/net-b to cross networks.
	c.serviceHostByName["ddns"] = "edge"
	s := types.ServiceSpec{Name: "tunneller", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh",
		Mesh: &types.MeshSpec{Port: 9090, AllowedServices: []string{"ddns"}}}
	errs := checkMesh(s, "bridge-01", c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must share a network")
}

// TestCheckMeshCrossPkiCallerRejected: an allowed_services caller that joins a
// DIFFERENT mesh than this callee is rejected. A foreign-mesh leaf cannot verify
// against this callee's trust bundle (each mesh has its own root), so admission
// would be impossible at runtime; without this guard a callee could name a
// foreign-mesh caller and, on a host running both meshes, admission would rest on
// physical co-location. Mirrors TestCheckGatewayPkiMismatch.
func TestCheckMeshCrossPkiCallerRejected(t *testing.T) {
	c := meshCtx()
	c.servicePkiByName["ddns"] = "other-mesh" // ddns now joins a different mesh
	s := types.ServiceSpec{Name: "tenants", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh",
		Mesh: &types.MeshSpec{Port: 8080, InternalPaths: []string{"/x/**"}, AllowedServices: []string{"ddns"}}}
	errs := checkMesh(s, "bridge-01", c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must share one pki")
}

// TestCheckMeshEgressRangeRejected: a mesh.port in the reserved mesh egress
// range would race the mesh proxy's loopback egress listeners.
func TestCheckMeshEgressRangeRejected(t *testing.T) {
	c := meshCtx()
	s := types.ServiceSpec{Name: "tunneller", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh",
		Mesh: &types.MeshSpec{Port: meshpaths.GatewayEgressPort, AllowedServices: nil}}
	errs := checkMesh(s, "bridge-01", c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "reserved mesh egress range")
}

// TestCheckMeshValid: a service exposing a mesh port with a resolvable allow list
// (including the reserved "gateway" token) passes.
func TestCheckMeshValid(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns", "gateway"}}), meshCtx())
	assert.Empty(t, errs)
}

// TestCheckMeshNoPathsRejected: a mesh block with neither public_paths nor
// internal_paths is rejected — the endpoint surface is allowlist-only (ADR-0034).
func TestCheckMeshNoPathsRejected(t *testing.T) {
	s := types.ServiceSpec{Name: "tenants", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh",
		Mesh: &types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}}}
	errs, _ := checkService(s, meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "allowlist-only")
}

// TestCheckMeshBadGlobRejected: a malformed path glob is rejected with the
// pathglob parse error.
func TestCheckMeshBadGlobRejected(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, PublicPaths: []string{"/a/**/b"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must be the final path segment")
}

// TestCheckMeshDuplicateGlobRejected: the same glob twice in one list is rejected.
func TestCheckMeshDuplicateGlobRejected(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, PublicPaths: []string{"/v1/**", "/v1/**"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "declared more than once")
}

// TestCheckMeshPublicInternalOverlapRejected: a path matching both a public and
// an internal glob has ambiguous edge visibility — rejected (D7).
func TestCheckMeshPublicInternalOverlapRejected(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080,
		PublicPaths: []string{"/v*/account/**"}, InternalPaths: []string{"/v1/**"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "edge visibility must be unambiguous")
}

// TestCheckServiceHealthPathsRules: the health listener is allowlist-only —
// a port without paths, paths without a port, and glob syntax are all rejected;
// a gateway-listed ingress-less service with both passes.
func TestCheckServiceHealthPathsRules(t *testing.T) {
	c := meshCtx()
	c.gatewayServiceTargets = map[string]bool{"tenants": true}
	c.gatewayHostKey = "bridge-01"
	c.gatewayHealthPort = 81

	// Gateway-listed, ingress-less, port + paths -> OK.
	s := meshSvc(&types.MeshSpec{Port: 8080})
	s.HealthProbesPort = 8081
	s.HealthProbePaths = []string{"/healthz"}
	errs, _ := checkService(s, c)
	assert.Empty(t, errs)

	// Port without paths -> FAIL (closed by default).
	s.HealthProbePaths = nil
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "declared without health_probe_paths")

	// Paths without the port -> FAIL.
	s.HealthProbesPort = 0
	s.HealthProbePaths = []string{"/healthz"}
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "declared without health_probes_port")

	// A glob in health_probe_paths -> FAIL (exact paths only).
	s.HealthProbesPort = 8081
	s.HealthProbePaths = []string{"/health/**"}
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "globs are not allowed")

	// Co-located with the gateway host, backend port == gateway public port -> FAIL.
	s.HealthProbePaths = []string{"/healthz"}
	s.HealthProbesPort = 81
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "equals the gateway's public health port")

	// Co-located with the gateway host, backend port on the gateway's own public
	// binds (:443 daemon TLS / :80 ACME) -> FAIL (they never live in portUsersByHost).
	s.HealthProbesPort = 443
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "held by the gateway's nginx")
	s.HealthProbesPort = 80
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "held by the gateway's nginx")

	// A probe path with a character nginx can never match (query separator) -> FAIL.
	s.HealthProbesPort = 8081
	s.HealthProbePaths = []string{"/healthz?probe=1"}
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), `contains "?"`)
	s.HealthProbePaths = []string{"/healthz"}

	// Backend health port in the preread loopback range on a host that runs an
	// EDGE nginx (here: the gateway host) -> FAIL, even though the surfacing tier
	// may be elsewhere; the terminators bind where the edge nginx runs.
	s.HealthProbesPort = nginx.LoopbackBase
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "reserved internal range")

	// Neither ingress nor gateway listing -> FAIL.
	c2 := meshCtx()
	s.HealthProbesPort = 8081
	errs, _ = checkService(s, c2)
	assert.Contains(t, strings.Join(errs, "|"), "listed in the scope gateway's services")
}

// TestCheckMeshRootGlobRejected: the root-equivalent "/**" is rejected — it
// matches every request path and would silently defeat the allowlist.
func TestCheckMeshRootGlobRejected(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, PublicPaths: []string{"/**"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "matches every request path")
}

// TestCheckMeshRequiresPki: a service with a mesh block but no pki: is rejected.
func TestCheckMeshRequiresPki(t *testing.T) {
	s := meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}})
	s.Pki = ""
	errs, _ := checkService(s, meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must declare pki:")
}

// TestCheckMeshBadPort: an out-of-range mesh.port is rejected.
func TestCheckMeshBadPort(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 0, AllowedServices: []string{"ddns"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "mesh.port")
}

// TestCheckMeshAllowUnknown: an allow-list name that is no service is rejected.
func TestCheckMeshAllowUnknown(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ghost"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not resolve to a service")
}

// TestCheckMeshAllowNonMesh: an allow-list name that is a service but not a mesh
// member (no pki:) is rejected.
func TestCheckMeshAllowNonMesh(t *testing.T) {
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"nopki"}}), meshCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "not a mesh member")
}

// TestCheckMeshAllowForbiddenDirection: a regional service naming a global service
// as a caller is rejected (regional→global only).
func TestCheckMeshAllowForbiddenDirection(t *testing.T) {
	c := meshCtx()
	c.forbiddenCallerNames = map[string]bool{"billing": true}
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"billing"}}), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "regional→global only")
}

// TestCheckMeshAllowCrossScopeCaller: a global service may name a regional caller
// (regional→global), resolved via callerCandidates.
func TestCheckMeshAllowCrossScopeCaller(t *testing.T) {
	c := meshCtx()
	c.meshServices = map[string]bool{}
	c.serviceNamesInScope = map[string]bool{}
	c.callerCandidates = map[string]bool{"ddns": true}
	c.callerPki = map[string]string{"ddns": "mesh"} // same mesh as the callee — allowed
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}}), c)
	assert.Empty(t, errs)
}

// TestCheckMeshCrossScopeCrossPkiCallerRejected: the same-pki guard also covers a
// CROSS-SCOPE caller — a regional caller in a different mesh than the global
// callee is rejected via the callerPki map (its leaf could never verify against
// the callee's trust bundle).
func TestCheckMeshCrossScopeCrossPkiCallerRejected(t *testing.T) {
	c := meshCtx()
	c.meshServices = map[string]bool{}
	c.serviceNamesInScope = map[string]bool{}
	// A cross-scope caller is not a same-scope service (checkCrossScopeNames
	// forbids a name in both scopes), so its pki resolves only via callerPki.
	c.servicePkiByName = map[string]string{}
	c.callerCandidates = map[string]bool{"ddns": true}
	c.callerPki = map[string]string{"ddns": "other-mesh"} // different mesh than the callee
	errs := checkMesh(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}}), "bridge-01", c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must share one pki")
}

// TestCheckMeshPortCollision: a mesh.port equal to another service's backend port on
// the host is rejected.
func TestCheckMeshPortCollision(t *testing.T) {
	c := meshCtx()
	c.targetUsersByHost = map[string]map[int][]string{"bridge-01": {8080: {"other"}}}
	errs, _ := checkService(meshSvc(&types.MeshSpec{Port: 8080, AllowedServices: []string{"ddns"}}), c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "belongs to a single service")
}

// A service may not run on a multi-instance compute: the host DNS / "<compute>.vm"
// record is derived from the bare compute name and can't address one instance.
func TestCheckServiceRejectsMultiInstanceHost(t *testing.T) {
	ctx := baseCtx()
	ctx.computeInstances = map[string]int{"bridge-01": 2}
	errs, _ := checkService(types.ServiceSpec{Name: "api", Host: "bridge", Type: "raw", User: "svc"}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "multi-instance")
}

func TestCheckServiceRejectsSpecKeyHost(t *testing.T) {
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{Name: "api", Host: "bridge-01", Type: "raw", User: "svc"}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "specKey")
}

// ingressFKCtx seeds a baseCtx with one ingress "web" co-located on the bridge
// host, so a service's ingress FK resolves and its routes are treated as
// co-located (ingress host == service host).
func ingressFKCtx() regionContext {
	c := baseCtx()
	c.ingressNames = map[string]bool{"web": true}
	c.ingressHost = map[string]string{"web": "bridge-01"}
	c.ingressNamesByHost = map[string][]string{"bridge-01": {"web"}}
	c.computeNetwork = map[string]string{"bridge-01": "net", "bridge": "net"}
	return c
}

func TestCheckServiceIngress(t *testing.T) {
	svc := func(in ...types.RouteSpec) types.ServiceSpec {
		return types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc", Ingress: "web", Routes: in}
	}

	// tls-termination route fronted by a resolvable ingress -> OK.
	errs, _ := checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ingressFKCtx())
	assert.Empty(t, errs)

	// A forward route is equally fine -> OK.
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353}), ingressFKCtx())
	assert.Empty(t, errs)

	// No routes (and no ingress) -> OK.
	errs, _ = checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc"}, baseCtx())
	assert.Empty(t, errs)

	// Routes without an ingress FK -> FAIL.
	errs, _ = checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}}, baseCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "must name the ingress")

	// An ingress FK that resolves to no ingress in scope -> FAIL.
	errs, _ = checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc", Ingress: "ghost",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}}, baseCtx())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "does not resolve to an ingress")
}

// TestCheckServiceCrossHostSameNetwork: a service whose backend host is on a
// different network than its ingress host fails the same-network rule; sharing a
// network passes.
func TestCheckServiceCrossHostSameNetwork(t *testing.T) {
	ctx := baseCtx()
	// Two hosts: bridge (backend) and edge (ingress).
	ctx.computeNames["edge"] = true
	ctx.computeKind["edge-01"] = "vm"
	ctx.computeCanonical["edge"] = "edge-01"
	ctx.computeCanonical["edge-01"] = "edge-01"
	ctx.ingressNames = map[string]bool{"web": true}
	ctx.ingressHost = map[string]string{"web": "edge-01"}
	ctx.computeNetwork = map[string]string{"bridge-01": "back-net", "bridge": "back-net", "edge-01": "edge-net", "edge": "edge-net"}

	svc := types.ServiceSpec{Name: "svc", Host: "bridge", Type: "raw", User: "svc", Ingress: "web",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}}
	errs, _ := checkService(svc, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "share a network")

	// Same network -> OK.
	ctx.computeNetwork["edge-01"] = "back-net"
	ctx.computeNetwork["edge"] = "back-net"
	errs, _ = checkService(svc, ctx)
	assert.Empty(t, errs)
}

// TestCheckServiceCrossHostTargetCollidesWithBackendListen: a cross-host service
// whose target port equals a public listen port held by nginx on its OWN backend
// host (because another ingress is co-located there) is rejected — the backend
// process could not bind that port. The collision check keys on the backend host,
// not the ingress host.
func TestCheckServiceCrossHostTargetCollidesWithBackendListen(t *testing.T) {
	ctx := baseCtx()
	// edge is the (different) ingress host; bridge is the backend host, which also
	// runs nginx (holding :9000) for some co-located ingress.
	ctx.computeNames["edge"] = true
	ctx.computeKind["edge-01"] = "vm"
	ctx.computeCanonical["edge"] = "edge-01"
	ctx.computeCanonical["edge-01"] = "edge-01"
	ctx.ingressNames = map[string]bool{"web": true}
	ctx.ingressHost = map[string]string{"web": "edge-01"} // cross-host: ingress on edge, service on bridge
	ctx.computeNetwork = map[string]string{"bridge-01": "net", "bridge": "net", "edge-01": "net", "edge": "net"}
	ctx.portUsersByHost = map[string]map[int][]string{"bridge-01": {9000: {"other"}}}
	ctx.targetUsersByHost = map[string]map[int][]string{}
	ctx.tlsTermIngressByHost = map[string]bool{}

	errs, _ := checkService(types.ServiceSpec{Name: "api", Host: "bridge", Type: "raw", User: "svc", Ingress: "web",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 9000}}}, ctx)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "collides with a public listen port on the backend host")
}

func TestCheckServiceIngressRules(t *testing.T) {
	base := func() regionContext {
		c := ingressFKCtx()
		c.portUsersByHost = map[string]map[int][]string{}
		c.forwardUsersByHost = map[string]map[int][]string{}
		c.targetUsersByHost = map[string]map[int][]string{}
		c.tlsTermIngressByHost = map[string]bool{}
		c.ingressHealthPort = map[string]int{}
		return c
	}
	svc := func(in ...types.RouteSpec) types.ServiceSpec {
		return types.ServiceSpec{Name: "svc", Host: "bridge", Type: "raw", User: "svc", Ingress: "web", Routes: in}
	}

	// A tls-termination + a forward route on one service is the bridge shape -> OK.
	ctx := base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}, 853: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ := checkService(svc(
		types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: []string{"key-broker.inforge.example.com"}},
		types.RouteSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353},
	), ctx)
	assert.Empty(t, errs)

	// An invalid type is rejected.
	errs, _ = checkService(svc(types.RouteSpec{Type: "passthrough", Listen: 443, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "is invalid")

	// listen is required on every route.
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "listen")

	// target is required on every route.
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "target")

	// listen and target must differ when co-located (nginx occupies the public port).
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 443}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must differ")

	// target collides with ANOTHER route's public listen port on the ingress host -> FAIL.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}, 8080: {"other"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "collides with a public listen port")

	// two different services binding the same backend target -> FAIL.
	ctx = base()
	ctx.targetUsersByHost["bridge-01"] = map[int][]string{8080: {"svc", "other"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "belongs to a single service")

	// vanity on a forward is meaningless -> FAIL.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{853: {"svc"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 853, Target: 5353, Vanity: []string{"x"}}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "remove vanity")

	// Two forwards share one port -> FAIL (one passthrough per port: a single map default).
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc", "other"}}
	ctx.forwardUsersByHost["bridge-01"] = map[int][]string{443: {"svc", "other"}}
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 443, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "single-service-exclusive")

	// A forward coexisting with a tls-termination on the SAME port -> OK (ssl_preread
	// demuxes: known SNIs to the terminator, the unknown SNI to the forward). Only
	// this service has the forward on 443, so it is the single map default.
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{443: {"svc", "other"}}
	ctx.forwardUsersByHost["bridge-01"] = map[int][]string{443: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 443, Target: 8080}), ctx)
	assert.Empty(t, errs)

	// A forward on :80 collides with a tls-termination on the ingress (ACME owns :80).
	ctx = base()
	ctx.portUsersByHost["bridge-01"] = map[int][]string{80: {"svc"}}
	ctx.tlsTermIngressByHost["bridge-01"] = true
	errs, _ = checkService(svc(types.RouteSpec{Type: types.IngressTypeForward, Listen: 80, Target: 8080}), ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "ACME owns :80")
}

func TestCheckServiceHealthRules(t *testing.T) {
	base := func() regionContext {
		c := ingressFKCtx()
		c.portUsersByHost = map[string]map[int][]string{}
		c.forwardUsersByHost = map[string]map[int][]string{}
		c.targetUsersByHost = map[string]map[int][]string{}
		c.tlsTermIngressByHost = map[string]bool{}
		c.ingressHealthPort = map[string]int{"web": 81}
		return c
	}
	svc := func(hp int, routes ...types.RouteSpec) types.ServiceSpec {
		return types.ServiceSpec{Name: "svc", Host: "bridge", Type: "raw", User: "svc", Ingress: "web", HealthProbesPort: hp, HealthProbePaths: []string{"/healthz"}, Routes: routes}
	}

	// A backend health port distinct from everything -> OK.
	errs, _ := checkService(svc(8081, types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), base())
	assert.Empty(t, errs)

	// Co-located backend health port == the ingress's public health port -> FAIL.
	errs, _ = checkService(svc(81), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "public health port")

	// Backend health port collides with the service's own route target -> FAIL.
	errs, _ = checkService(svc(8080, types.RouteSpec{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "route target")

	// Co-located health port in the reserved internal loopback range -> FAIL.
	errs, _ = checkService(svc(nginx.LoopbackBase), base())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "reserved internal range")

	// Backend health port collides with a public listen port on the backend host -> FAIL.
	pubCtx := base()
	pubCtx.portUsersByHost["bridge-01"] = map[int][]string{9999: {"other"}}
	errs, _ = checkService(svc(9999), pubCtx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "public listen port")

	// Backend health port is another service's backend target on the same host -> FAIL.
	tgtCtx := base()
	tgtCtx.targetUsersByHost["bridge-01"] = map[int][]string{9998: {"other"}}
	errs, _ = checkService(svc(9998), tgtCtx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "belongs to a single service")

	// A health endpoint without an ingress -> FAIL.
	s := svc(8081)
	s.Ingress = ""
	errs, _ = checkService(s, base())
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "|"), "must name the ingress")
}

func TestCheckIngressHealthPort(t *testing.T) {
	base := func(healthPort int) (types.IngressSpec, regionContext) {
		// baseCtx already resolves "bridge" -> single-instance vm "bridge-01".
		c := baseCtx()
		c.portUsersByHost = map[string]map[int][]string{"bridge-01": {443: {"svc"}}}
		return types.IngressSpec{Name: "edge", Container: "bridge", Host: "bridge", HealthProbesPort: healthPort}, c
	}

	// Health port 81, distinct from the 443 listen -> no health error.
	s, ctx := base(81)
	errs, _ := checkIngress(s, ctx)
	assert.NotContains(t, strings.Join(errs, "|"), "health_probes_port")

	// Health port equals a route listen on this host -> FAIL.
	s, ctx = base(443)
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "collides with a route listen port")

	// Health port 80 is reserved for ACME -> FAIL.
	s, ctx = base(80)
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "must not be 80")

	// Health port in the reserved ssl_preread loopback range -> FAIL.
	s, ctx = base(nginx.LoopbackBase)
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "reserved internal range")

	// Two ingresses sharing one compute host -> FAIL.
	s, ctx = base(81)
	ctx.ingressNamesByHost = map[string][]string{"bridge-01": {"other-ingress", "edge"}}
	errs, _ = checkIngress(s, ctx)
	assert.Contains(t, strings.Join(errs, "|"), "hosts at most one ingress")
}

// TestCheckServiceLoopbackRangeCrossHostIngress: the route-target reserved-loopback
// check is gated on the backend host running an edge nginx (not only on co-location
// with the service's own ingress). A service whose backend host independently hosts
// a DIFFERENT ingress still can't bind a loopback-terminator port there.
func TestCheckServiceLoopbackRangeCrossHostIngress(t *testing.T) {
	c := baseCtx()
	c.computeNetwork = map[string]string{"bridge-01": "net", "bridge": "net", "edge-01": "net", "edge": "net"}
	c.computeNames["edge"] = true
	c.computeCanonical["edge"] = "edge-01"
	c.computeKind["edge-01"] = "vm"
	// The service's ingress "web" is on edge-01 (cross-host), but the service's OWN
	// host bridge-01 independently hosts ingress "other" — so nginx runs there.
	c.ingressNames = map[string]bool{"web": true, "other": true}
	c.ingressHost = map[string]string{"web": "edge-01", "other": "bridge-01"}
	c.ingressNamesByHost = map[string][]string{"edge-01": {"web"}, "bridge-01": {"other"}}

	s := types.ServiceSpec{Name: "api", Host: "bridge", Type: "raw", User: "svc", Ingress: "web",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: nginx.LoopbackBase}}}
	errs, _ := checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "reserved internal range",
		"a backend on a host that independently runs an ingress cannot bind a loopback-terminator port")
}

// TestCheckServiceNginxImplicitBinds: a co-located backend bind (exposed port,
// mesh.port, or route target) that lands on an IMPLICIT public port the edge nginx
// holds — ACME :80, an app/gateway :443, or the public health listener, none of
// which are route listens in portUsersByHost — is rejected.
func TestCheckServiceNginxImplicitBinds(t *testing.T) {
	// exposed_ports tcp/443 hitting an app's :443 TLS listener on the same host.
	c := baseCtx()
	c.nginxBindsByHost = map[string]map[int]string{"bridge-01": {443: "app TLS listener"}}
	s := types.ServiceSpec{Name: "tunneller", Host: "bridge", Type: "raw", User: "svc",
		ExposedPorts: []types.ExposedPort{{Proto: "tcp", Port: 443}}}
	errs, _ := checkService(s, c)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "|"), "held by the edge nginx")

	// mesh.port hitting ACME :80 on the same host.
	mc := meshCtx()
	mc.nginxBindsByHost = map[string]map[int]string{"bridge-01": {80: "ACME HTTP-01 challenge/redirect"}}
	ms := types.ServiceSpec{Name: "tenants", Host: "bridge", Type: "raw", User: "svc", Pki: "mesh",
		Mesh: &types.MeshSpec{Port: 80, InternalPaths: []string{"/x/**"}}}
	errs = checkMesh(ms, "bridge-01", mc)
	require.NotEmpty(t, errs)
	assert.Contains(t, strings.Join(errs, "|"), "held by the edge nginx")

	// A route target hitting the public health listener the ingress host binds.
	rc := ingressFKCtx()
	rc.nginxBindsByHost = map[string]map[int]string{"bridge-01": {8081: "public health listener"}}
	rs := types.ServiceSpec{Name: "api", Host: "bridge", Type: "raw", User: "svc", Ingress: "web",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8081}}}
	errs, _ = checkService(rs, rc)
	assert.Contains(t, strings.Join(errs, "|"), "held by the edge nginx")
}

// TestCheckMeshEgressRangeOnPublicBinds: a PUBLIC bind (route listen or the
// ingress/gateway public health port) in the reserved mesh egress range collides
// with the mesh proxy's loopback egress listeners — but only on a host that runs a
// mesh proxy. Gated accordingly (unlike backend binds, which are always on a mesh
// host).
func TestCheckMeshEgressRangeOnPublicBinds(t *testing.T) {
	egressPort := meshpaths.EgressBase

	// Route listen in the range, ingress host runs a mesh proxy -> FAIL.
	c := ingressFKCtx()
	c.meshHosts = map[string]bool{"bridge-01": true}
	s := types.ServiceSpec{Name: "api", Host: "bridge", Type: "raw", User: "svc", Ingress: "web",
		Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: egressPort, Target: 8080}}}
	errs, _ := checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "reserved mesh egress range")

	// Same route listen, ingress host runs NO mesh proxy -> OK (no proxy to collide).
	c2 := ingressFKCtx()
	errs, _ = checkService(s, c2)
	assert.NotContains(t, strings.Join(errs, "|"), "reserved mesh egress range")

	// Ingress public health port in the range, on a mesh host -> FAIL.
	ic := regionContext{
		computeNames: map[string]bool{"bridge": true}, computeCanonical: map[string]string{"bridge": "bridge-01", "bridge-01": "bridge-01"},
		computeKind: map[string]string{"bridge-01": "vm"}, computeDeployer: map[string]bool{"bridge-01": true},
		available: map[string]bool{"hetzner": true}, meshHosts: map[string]bool{"bridge-01": true},
	}
	ierrs, _ := checkIngress(types.IngressSpec{Name: "web", Host: "bridge", HealthProbesPort: egressPort}, ic)
	assert.Contains(t, strings.Join(ierrs, "|"), "reserved mesh egress range")
}

func TestCheckServiceExposedPorts(t *testing.T) {
	// A private-only service: no ingress, no routes, just exposed_ports.
	svc := func(eps ...types.ExposedPort) types.ServiceSpec {
		return types.ServiceSpec{Name: "tunneller", Host: "bridge", Type: "raw", User: "svc", ExposedPorts: eps}
	}
	tcp := func(p int) types.ExposedPort { return types.ExposedPort{Proto: "tcp", Port: p} }
	udp := func(p int) types.ExposedPort { return types.ExposedPort{Proto: "udp", Port: p} }

	// Private-only service with no ingress -> OK (exposed_ports require no ingress).
	errs, _ := checkService(svc(tcp(9444)), baseCtx())
	assert.Empty(t, errs)

	// tcp/N and udp/N coexist on one service (distinct OS binds) -> OK.
	errs, _ = checkService(svc(tcp(9444), udp(9444)), baseCtx())
	assert.Empty(t, errs)

	// Duplicate (proto, port) on the same service -> FAIL.
	errs, _ = checkService(svc(tcp(9444), tcp(9444)), baseCtx())
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "declared more than once")

	// tcp exposed port equals the service's own route target -> FAIL.
	s := svc(tcp(8080))
	s.Ingress = "web"
	s.Routes = []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080}}
	errs, _ = checkService(s, ingressFKCtx())
	assert.Contains(t, strings.Join(errs, "|"), "route target")

	// tcp exposed port equals the service's own health_probes_port -> FAIL.
	s = svc(tcp(8081))
	s.Ingress = "web"
	s.HealthProbesPort = 8081
	c := ingressFKCtx()
	c.ingressHealthPort = map[string]int{"web": 81}
	errs, _ = checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "health_probes_port")

	// tcp exposed port equals a public listen port on the backend host -> FAIL.
	pubCtx := baseCtx()
	pubCtx.portUsersByHost = map[string]map[int][]string{"bridge-01": {9999: {"other"}}}
	errs, _ = checkService(svc(tcp(9999)), pubCtx)
	assert.Contains(t, strings.Join(errs, "|"), "public listen port")

	// tcp exposed port is another service's backend target on the same host -> FAIL.
	tgtCtx := baseCtx()
	tgtCtx.targetUsersByHost = map[string]map[int][]string{"bridge-01": {9998: {"other"}}}
	errs, _ = checkService(svc(tcp(9998)), tgtCtx)
	assert.Contains(t, strings.Join(errs, "|"), "belongs to a single service")

	// udp exposed port is another service's udp exposed port on the same host -> FAIL.
	udpCtx := baseCtx()
	udpCtx.udpExposedUsersByHost = map[string]map[int][]string{"bridge-01": {9444: {"other"}}}
	errs, _ = checkService(svc(udp(9444)), udpCtx)
	assert.Contains(t, strings.Join(errs, "|"), "belongs to a single service")

	// udp exposed port does NOT collide with another service's TCP backend target -> OK.
	mixCtx := baseCtx()
	mixCtx.targetUsersByHost = map[string]map[int][]string{"bridge-01": {9444: {"other"}}}
	errs, _ = checkService(svc(udp(9444)), mixCtx)
	assert.Empty(t, errs)

	// tcp exposed port in the reserved loopback range when the host runs an ingress -> FAIL.
	loopCtx := baseCtx()
	loopCtx.ingressNamesByHost = map[string][]string{"bridge-01": {"edge"}}
	errs, _ = checkService(svc(tcp(nginx.LoopbackBase)), loopCtx)
	assert.Contains(t, strings.Join(errs, "|"), "reserved internal range")

	// Same reserved-range port, but the host runs no ingress -> OK (no terminator).
	errs, _ = checkService(svc(tcp(nginx.LoopbackBase)), baseCtx())
	assert.Empty(t, errs)

	// An invalid proto -> FAIL.
	errs, _ = checkService(svc(types.ExposedPort{Proto: "sctp", Port: 9444}), baseCtx())
	assert.Contains(t, strings.Join(errs, "|"), "proto")
}

// TestCheckServiceHealthCrossHostNetwork: a health-only service (no routes) whose
// host is on a different network than its ingress is rejected — the same-network
// rule now covers health, not just routes.
func TestCheckServiceHealthCrossHostNetwork(t *testing.T) {
	c := baseCtx()
	c.computeNames = map[string]bool{"bridge": true, "gateway": true}
	c.computeCanonical = map[string]string{"bridge-01": "bridge-01", "bridge": "bridge-01", "gateway-01": "gateway-01", "gateway": "gateway-01"}
	c.computeKind = map[string]string{"bridge-01": "vm", "gateway-01": "vm"}
	c.computeDeployer = map[string]bool{"gateway-01": true}
	c.computeNetwork = map[string]string{"bridge-01": "net", "gateway-01": "othernet"}
	c.ingressNames = map[string]bool{"web": true}
	c.ingressHost = map[string]string{"web": "bridge-01"}
	c.ingressHealthPort = map[string]int{"web": 81}
	c.portUsersByHost = map[string]map[int][]string{}
	c.targetUsersByHost = map[string]map[int][]string{}

	// gateway (othernet) hosts the service; ingress web is on bridge (net) -> FAIL.
	s := types.ServiceSpec{Name: "probe", Host: "gateway", Type: "raw", User: "probe", Ingress: "web", HealthProbesPort: 8081}
	errs, _ := checkService(s, c)
	assert.Contains(t, strings.Join(errs, "|"), "share a network")
}

func TestCheckServiceDeployUser(t *testing.T) {
	// A service whose host declares no deploy_user can't be provisioned over SSH.
	ctx := baseCtx()
	ctx.computeDeployer = map[string]bool{"bridge-01": false}
	errs, _ := checkService(types.ServiceSpec{Host: "bridge", Type: "raw", User: "svc"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "no deploy_user")
}

func TestCheckServiceUser(t *testing.T) {
	// A service that declares no user has no account for the agent to drop
	// privilege to before exec.
	ctx := baseCtx()
	errs, _ := checkService(types.ServiceSpec{Host: "bridge", Type: "raw"}, ctx)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "must declare the no-login user")
}

// A deploy_user name lands unquoted in file paths and a sudoers rule inside the
// root-run first-boot script, so anything outside the login-name charset is an
// author error, not something to escape and boot with.
func TestCheckComputeDeployUserCharset(t *testing.T) {
	ctx := baseCtx()
	ctx.sizeTable = sizes.DefaultTable()
	ctx.networks = map[string]types.NetworkSpec{"corenet": {Name: "corenet"}}
	base := types.ComputeSpec{Provider: "hetzner", Network: "corenet", Size: "SMALL", Kind: "vm"}

	ok := base
	ok.DeployUser = &types.DeployUserSpec{Name: "deploy"}
	errs, _ := checkCompute(ok, ctx)
	assert.Empty(t, errs)

	for _, name := range []string{`deploy'; touch /pwn; '`, "de ploy", "Deploy", "1deploy", "deploy\nroot"} {
		bad := base
		bad.DeployUser = &types.DeployUserSpec{Name: name}
		errs, _ := checkCompute(bad, ctx)
		require.NotEmpty(t, errs, "deploy_user %q must be rejected", name)
		assert.Contains(t, strings.Join(errs, "\n"), "deploy_user:")
	}
}

// A vanity FQDN on another domain is not a record on that domain: records are
// created zone-relative inside the DNS authority's zone, so it deploys as
// "<fqdn>.<base_domain>" — a wrong hostname whose ACME cert can never issue.
func TestCheckVanityZone(t *testing.T) {
	svc := func(vanity ...string) types.Resources {
		return types.Resources{Service: []types.ServiceSpec{{
			Name:   "api",
			Routes: []types.RouteSpec{{Type: types.IngressTypeTLSTermination, Listen: 443, Target: 8080, Vanity: vanity}},
		}}}
	}

	cases := []struct {
		name   string
		vanity []string
		fail   bool
	}{
		{"bare token is derived in-zone", []string{"api"}, false},
		{"literal under base_domain", []string{"account.example.com"}, false},
		{"the apex itself", []string{"example.com"}, false},
		{"base_domain template", []string{"account.{ENV}.{BASE_DOMAIN}"}, false},
		{"out-of-zone literal", []string{"shop.other.net"}, true},
		{"look-alike suffix", []string{"shop.notexample.com"}, true},
		{"one bad among good", []string{"account.example.com", "shop.other.net"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &reporter{}
			checkVanityZone(r, svc(c.vanity...), "regional", "example.com")
			assert.Equal(t, c.fail, r.failed)
		})
	}

	t.Run("unresolved base_domain is not checked", func(t *testing.T) {
		r := &reporter{}
		checkVanityZone(r, svc("shop.other.net"), "regional", "${BASE_DOMAIN}")
		assert.False(t, r.failed)
	})
}

// The mint derives the group roles <db>_ro/<db>_rw from the database name. An owner:
// colliding with one of them would be GRANTed to the service holding that permission,
// making it a member of the role that owns the database and everything in it.
func TestCheckDatabaseRejectsOwnerCollidingWithGroupRole(t *testing.T) {
	ctx := regionContext{clusterNames: map[string]bool{"pg": true}}
	disabled := false
	base := types.DatabaseSpec{Cluster: "pg", Database: "app", Backup: types.BackupPolicy{Enabled: &disabled}}

	for _, owner := range []string{"app_ro", "app_rw"} {
		s := base
		s.Owner = owner
		errs, _ := checkDatabase(s, ctx)
		if len(errs) == 0 {
			t.Errorf("owner %q must be rejected (it is a derived group role)", owner)
		}
	}
	ok := base
	ok.Owner = "app_owner"
	if errs, _ := checkDatabase(ok, ctx); len(errs) != 0 {
		t.Errorf("a non-colliding owner must validate, got %v", errs)
	}
}

// Postgres silently truncates identifiers at 63 bytes, so a long database: name yields
// a physical database — and derived <db>_ro/<db>_rw group roles — under a name the
// deployment never uses. `inforge validate` must reject it credential-free, at PR time.
func TestCheckDatabaseRejectsTruncatedIdentifiers(t *testing.T) {
	ctx := regionContext{clusterNames: map[string]bool{"pg": true}}
	disabled := false
	base := types.DatabaseSpec{Cluster: "pg", Owner: "app", Backup: types.BackupPolicy{Enabled: &disabled}}

	// 61 bytes: the database name itself fits, but "<db>_rw" is 64 — over the limit.
	tooLong := base
	tooLong.Database = strings.Repeat("d", pgrole.MaxIdentifierLen-2)
	if errs, _ := checkDatabase(tooLong, ctx); len(errs) == 0 {
		t.Error("a database whose derived group roles exceed 63 bytes must be rejected")
	}
	// 64 bytes: the database name itself is over the limit.
	over := base
	over.Database = strings.Repeat("d", pgrole.MaxIdentifierLen+1)
	if errs, _ := checkDatabase(over, ctx); len(errs) == 0 {
		t.Error("a database name over 63 bytes must be rejected")
	}
	// 60 bytes: "<db>_rw" is exactly 63 — legal.
	fits := base
	fits.Database = strings.Repeat("d", pgrole.MaxIdentifierLen-3)
	if errs, _ := checkDatabase(fits, ctx); len(errs) != 0 {
		t.Errorf("a database at the limit must validate, got %v", errs)
	}
}
