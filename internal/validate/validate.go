// Package validate checks a project's resource definitions for one environment:
// each file against its embedded JSON Schema, plus the semantic and
// cross-resource rules that a schema cannot express (CIDR hierarchy, foreign
// keys against expanded compute specKeys, the secrets source DSL, provider
// availability). It prints an OK/FAIL line per file and returns an error if any
// file failed.
package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/wardnet/inforge/internal/agent"
	"github.com/wardnet/inforge/internal/grant"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/nginx"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/sizes"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/schemas"
	"gopkg.in/yaml.v3"
)

// reporter accumulates pass/fail state while printing per-file results.
type reporter struct {
	failed bool
}

func (r *reporter) report(path string, errs, warns []string) {
	for _, w := range warns {
		fmt.Printf("WARN %s\n     %s\n", path, w)
	}
	if len(errs) > 0 {
		r.failed = true
		fmt.Printf("FAIL %s\n", path)
		for _, e := range errs {
			fmt.Printf("     %s\n", e)
		}
		return
	}
	fmt.Printf("OK   %s\n", path)
}

// fail records a standalone failure not tied to a resource file.
func (r *reporter) fail(label string, msgs ...string) {
	r.failed = true
	fmt.Printf("FAIL %s\n", label)
	for _, m := range msgs {
		fmt.Printf("     %s\n", m)
	}
}

// fileOf is a resource file read both as a raw document (for schema validation)
// and as a typed, default-normalised spec (for semantic checks).
type fileOf[T any] struct {
	path     string
	raw      any
	spec     T
	parseErr error
}

// readFolders reads every named sub-folder under dir: each folder must contain
// a manifest.yaml. The path field of the returned fileOf is set to the
// manifest.yaml path. A missing directory yields nil (same as readFiles).
func readFolders[T any](dir string) ([]fileOf[T], []string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []fileOf[T]
	var folders []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		folder := filepath.Join(dir, e.Name())
		manifest := filepath.Join(folder, "manifest.yaml")
		b, err := os.ReadFile(manifest)
		f := fileOf[T]{path: manifest}
		if os.IsNotExist(err) {
			f.parseErr = fmt.Errorf("resource folder %q has no manifest.yaml", folder)
			out = append(out, f)
			folders = append(folders, folder)
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", manifest, err)
		}
		if err := yaml.Unmarshal(b, &f.raw); err != nil {
			f.parseErr = err
			out = append(out, f)
			folders = append(folders, folder)
			continue
		}
		if err := yaml.Unmarshal(b, &f.spec); err != nil {
			f.parseErr = err
		}
		out = append(out, f)
		folders = append(folders, folder)
	}
	return out, folders, nil
}

// globalRefs carries the global slice's referenceable outputs so a regional
// secrets `ref:` may resolve a `global/<name>` target. It holds the global
// database names and expanded compute specKeys; the regional validation context
// is seeded with these under a `global/` prefix (see validateResourceSet). It is
// nil for the global slice's own validation pass, which runs in a global-only
// context so that a global resource referencing a regional one fails as
// not-found (enforcing "global → global only").
type globalRefs struct {
	databaseNames map[string]bool   // bare global database name -> true
	computeKind   map[string]string // accepted global compute FK form -> kind
	pkiTopology   map[string]string // bare global PKI resource name -> topology
	// meshServices holds the bare names of global services that are mesh members
	// (declare pki:). Seeded into the regional pass so a regional service's mesh
	// allowed_services that names a global service can be reported as the forbidden
	// direction (a global service may only call other global services — a regional
	// service cannot have a global caller).
	meshServices map[string]bool
}

// buildGlobalRefs derives the cross-referenceable outputs from the loaded
// global resource set (already default-normalised by the loader). Single-instance
// computes are additionally keyed by their bare name, mirroring CanonicalComputeKeys.
func buildGlobalRefs(global types.Resources) *globalRefs {
	g := &globalRefs{databaseNames: map[string]bool{}, computeKind: map[string]string{}, pkiTopology: map[string]string{}, meshServices: map[string]bool{}}
	for _, d := range global.Database {
		g.databaseNames[d.Name] = true
	}
	for _, s := range global.Service {
		if s.Pki != "" {
			g.meshServices[s.Name] = true
		}
	}
	for _, c := range global.Compute {
		for i := 1; i <= c.InstanceCount; i++ {
			g.computeKind[naming.SpecKey(c.Name, i)] = c.Kind
		}
		if c.InstanceCount == 1 {
			g.computeKind[c.Name] = c.Kind
		}
	}
	for _, p := range global.PKI {
		g.pkiTopology[p.Name] = p.Topology
	}
	return g
}

// globalNeedsDNS reports whether the global slice realizes any DNS record — i.e.
// whether it needs a DNS authority on its placement region. The deploy layer's
// derivedRecords emits a host (<compute>.vm) A-record for EVERY compute, a service
// (<svc>.svc) record + vanity for every ingress-bearing service, and an app record
// for every app; services, ingress, and apps all sit on a compute host (same-scope
// host/ingress FKs). So within a single slice "realizes a record" is exactly
// "declares a compute" — checking compute alone stays in lockstep with
// derivedRecords without re-deriving it (avoiding a validate→program import).
func globalNeedsDNS(g types.Resources) bool {
	return len(g.Compute) > 0
}

// regionContext holds the foreign-key targets and tables a region's semantic
// checks resolve against.
type regionContext struct {
	available        map[string]bool
	sizeTable        sizes.Table
	networks         map[string]types.NetworkSpec // specKey -> network
	computeKind      map[string]string            // expanded specKey -> kind
	computeCanonical map[string]string            // any accepted compute FK form -> canonical specKey
	computeInstances map[string]int               // canonical specKey -> the compute's instance_count
	computeDeployer  map[string]bool              // canonical specKey -> declares a deploy_user
	computeNames     map[string]bool              // bare compute names; service.host must be one of these
	computeNetwork   map[string]string            // canonical specKey (and bare name) -> network name; for the cross-host same-network check
	databaseNames    map[string]bool
	// ingressNames holds the ingress resource names declared in this scope. An
	// app's or service's `ingress:` foreign key must resolve to one of them —
	// same-scope only, exactly like a service's host must be a compute in the same
	// set (a global/ prefix is rejected in checkApp/checkService). Name *uniqueness*
	// is enforced generically in validateType; this map only answers FK existence.
	ingressNames map[string]bool
	// ingressHost maps an ingress resource name to the canonical specKey of its
	// host compute, so checkService can decide whether a service's routes are
	// co-located with their ingress (and, when not, enforce the same-network rule).
	// An ingress whose host FK does not resolve is absent (checkIngress reports it).
	ingressHost map[string]string
	// appSubdomainCounts counts app subdomain declarations per value in this scope so
	// checkApp can flag a collision: two apps sharing a subdomain would resolve to
	// the same public FQDN. (Bare-name uniqueness is handled generically in
	// validateType; the subdomain is a distinct, app-specific field.)
	appSubdomainCounts map[string]int
	// pkiResources maps a PKI resource name (a grant target, "<name>" or the
	// "global/<name>" form for the global slice's resources) to its topology. A
	// grant on a pki/<name> target resolves against this map; a regional service
	// reaches its own region's PKI resources or a global/<name> one (the
	// cross-region boundary, mirrored from databaseNames).
	pkiResources map[string]string
	// portUsersByHost maps a canonical INGRESS host specKey to, per listen port, the
	// names of the services with a route on that port (where the ingress nginx
	// holds the public port). It is the public-port collision oracle (e.g. a backend
	// target may not equal a public listen port on its host).
	portUsersByHost map[string]map[int][]string
	// forwardUsersByHost maps a canonical INGRESS host specKey to, per listen port,
	// the names of the services with a FORWARD route on that port. A forward port is
	// single-service-exclusive — at most one passthrough per (ingress host, port) —
	// but a forward MAY coexist with tls-termination routes on the same port (nginx
	// ssl_preread demuxes known SNIs to the terminators and the unknown SNI to the
	// forward). So exclusivity is enforced against forwards only, not all routes.
	forwardUsersByHost map[string]map[int][]string
	// ingressHealthPort maps an ingress resource name to the public health port it
	// exposes (its health_probes_port, defaulting to 81) so checkService/checkIngress
	// can enforce the health-port collision rules.
	ingressHealthPort map[string]int
	// ingressNamesByHost maps a canonical compute host specKey to the ingress resources
	// that reference it. A host may host at most one ingress — the derived nginx config,
	// firewall, and health port are per-host, so two ingresses on one host would silently
	// merge/override. checkIngress rejects a shared host.
	ingressNamesByHost map[string][]string
	// targetUsersByHost maps a canonical BACKEND host specKey (the service's own
	// host) to, per target port, the names of the services binding it. A target must
	// belong to a single service on its host, and — only when the service is
	// co-located with its ingress — must not collide with a public listen port nginx
	// holds on that host (see checkService).
	targetUsersByHost map[string]map[int][]string
	// udpExposedUsersByHost maps a canonical BACKEND host specKey to, per UDP port,
	// the names of services declaring that udp exposed_port (ADR-0029). UDP exposed
	// ports are tracked separately from the int-keyed (implicitly-TCP) target space
	// so tcp/N and udp/N may coexist (distinct OS binds); a udp exposed port collides
	// only with another service's udp exposed port. TCP exposed ports share
	// targetUsersByHost with route targets and health ports.
	udpExposedUsersByHost map[string]map[int][]string
	// tlsTermIngressByHost marks a canonical INGRESS host specKey that has at least
	// one tls-termination route across the services it fronts — so a forward on :80
	// (which ACME needs) can be rejected.
	tlsTermIngressByHost map[string]bool
	// gatewayScopeCount is the number of gateway resources declared in this scope.
	// A gateway is a scope singleton (≤1 per scope): it is the scope's one public
	// daemon edge and its config is derived per-scope, so a second one is ambiguous.
	// checkGateway rejects the scope when this exceeds 1.
	gatewayScopeCount int
	// serviceAllowsGateway records whether a service permits the north-south gateway to
	// call it (its mesh.allowed_services contains "gateway"). checkGateway requires a
	// route's target service to allow the gateway (the two sides must agree, ADR-0032).
	serviceAllowsGateway map[string]bool
	// servicePkiByName maps each service name in this scope to its pki: mesh membership.
	// checkGateway requires every route target to join the SAME mesh as the gateway —
	// a callee's trust bundle only admits callers chaining to its own mesh's
	// intermediates, so a mixed-mesh gateway route could never complete a handshake.
	servicePkiByName map[string]string
	// serviceNamesInScope holds every service name declared in this scope (regardless
	// of pki:), and meshServices the subset that are mesh members (declare pki:). A
	// gateway route's allowed_services caller must resolve to a mesh member — these two
	// maps separate "not a service" from "a service but not a mesh member" for a precise
	// message.
	serviceNamesInScope map[string]bool
	meshServices        map[string]bool
	// callerCandidates holds cross-scope service names that ARE valid callers of a
	// gateway route in this scope: regional mesh services in the global pass (regional→
	// global is allowed), empty in the regional pass. forbiddenCallerNames holds
	// cross-scope service names that exist but are NOT valid callers here: global mesh
	// services in the regional pass (a global service may only call other global
	// services), empty in the global pass. Together they let a gateway route's
	// allowed_services resolution enforce the ADR-0013 direction rule with a precise
	// message.
	callerCandidates     map[string]bool
	forbiddenCallerNames map[string]bool
	// encStore is the environment's committed encrypted secret store
	// (resources/<env>/secrets.enc.yaml), nil when the file does not exist. A
	// `vault:<KEY>` secret on a service must have a ciphertext under
	// (container, KEY); the check is presence-only so validation stays
	// credential-free.
	encStore *secretstore.Store
	// providerDefaults are the project-level provider fallbacks applied when a
	// spec omits its provider field (resolved via types.ResolveProvider).
	providerDefaults types.ProviderDefaults
}

// ValidateResources validates the single shared resource set under <dir>/<env>/
// and returns an error if any file failed. The resource set is defined once and
// instantiated into every region in regions.yaml, so its schema and foreign-key
// graph are region-independent and checked once; provider availability is then
// checked per region (a resource's provider must be declared in every region the
// set deploys into).
func ValidateResources(env, dir string, defaults types.ProviderDefaults) error {
	schemaSet, err := compileSchemas()
	if err != nil {
		return fmt.Errorf("compile schemas: %w", err)
	}

	// Structural validation must run without credentials, so load the region table
	// raw — ${ENV_VAR} references are left as literals rather than substituted:
	// validation checks the region/provider structure, not credential values, and
	// must not be tripped by an unset credential (e.g. an unresolved zone ref must
	// not read as an empty, "missing", zone).
	regionTable, global, err := loader.LoadRegionTableRaw(env, dir)
	if err != nil {
		return err
	}
	sizeTable, err := loader.LoadSizeTable(env, dir)
	if err != nil {
		return err
	}
	vars, err := loader.LoadVariablesLenient(env, dir)
	if err != nil {
		return err
	}
	// The global slice's referenceable outputs seed the regional context so a
	// regional secrets `ref:` may resolve a global/<name> database/compute target.
	globalRes, err := loader.LoadGlobalResources(env, dir)
	if err != nil {
		return err
	}
	// The regional slice's mesh service names seed the global pass so a global
	// service's gateway route may name a regional service in allowed_services
	// (regional→global is the one permitted call direction, ADR-0013). Loaded
	// leniently: a malformed regional set fails its own pass with a precise per-file
	// error, so here an error just yields an empty caller set rather than aborting.
	regionalMeshServices := map[string]bool{}
	if regionalRes, rerr := loader.LoadResources(env, dir); rerr == nil {
		for _, s := range regionalRes.Service {
			if s.Pki != "" {
				regionalMeshServices[s.Name] = true
			}
		}
	}

	r := &reporter{}
	base := filepath.Join(dir, env)
	regionalBase := filepath.Join(base, "regional")
	// The encrypted secret store is optional (absent until `inforge secret init`);
	// a present-but-broken store is reported against its own path so the rest of
	// the resource set still validates. One env-scoped store serves both slices —
	// secrets are container-keyed, not region-keyed.
	encStore, err := secretstore.Load(secretstore.Path(dir, env))
	if err != nil && !errors.Is(err, secretstore.ErrNotFound) {
		r.fail(secretstore.Path(dir, env), err.Error())
		encStore = nil
	}
	// The PKI store is optional and read credential-free (only its plaintext
	// structure — names, topology, which scopes have intermediates), exactly like
	// the secret store above.
	pkiStore, err := pki.Load(pki.Path(dir, env))
	if err != nil && !errors.Is(err, pki.ErrNotFound) {
		r.fail(pki.Path(dir, env), err.Error())
		pkiStore = nil
	}
	globalBase := filepath.Join(base, "global")
	checkVariables(r, vars, filepath.Join(base, "variables.yaml"))
	checkRegionsFile(r, regionTable, global, filepath.Join(base, "regions.yaml"))

	// Validate the global slice in a GLOBAL-ONLY context (globalRefs nil): its FK
	// graph resolves only against global resources, so a global resource
	// referencing a regional one fails as not-found — enforcing "global → global
	// only". A global slice with resources but no global providers block is an error.
	if err := validateResourceSet(r, schemaSet, globalBase, nil, sizeTable, nil, regionalMeshServices, encStore, defaults); err != nil {
		return err
	}
	if global == nil && globalRes.HasAny() {
		r.fail("regions.yaml [global]", "resources/"+env+"/global declares resources but regions.yaml has no global providers block")
	}
	// A global compute (and the service/ingress records it carries) realizes DNS
	// records and ACME certs against the placement region's DNS authority (ADR-0023)
	// — global slices have no region of their own. If that region declares no dns:
	// block, those records and certs would silently never deploy, so reject the slice
	// at validate time. Only fire when the placement region actually exists: a
	// missing/typo'd placementRegion indexes the table to a zero-value (nil Dns) and
	// would otherwise pile a misleading "no dns: authority" error on top of the real
	// "placementRegion not defined" error checkRegionsFile already reports.
	if global != nil && globalNeedsDNS(globalRes) {
		if pr, ok := regionTable[global.PlacementRegion]; ok && pr.Dns == nil {
			r.fail("regions.yaml [global]", fmt.Sprintf(
				"global slice declares a compute/service/ingress that needs DNS (host records, tls-termination, "+
					"health probes, or an ingress) but placementRegion %q has no dns: authority — declare a dns: block "+
					"on that region in regions.yaml", global.PlacementRegion))
		}
	}

	// Validate the shared regional set once: schema + the region-independent FK
	// graph, with the global outputs injected so a regional secrets `ref:` may
	// resolve a global/<name> target. Provider availability is region-specific, so
	// it is skipped here (available nil) and checked separately per region below.
	if err := validateResourceSet(r, schemaSet, regionalBase, nil, sizeTable, buildGlobalRefs(globalRes), nil, encStore, defaults); err != nil {
		return err
	}

	// Mesh PKI references: a global service's leaf comes from the global
	// intermediate; a regional service's leaf, from each region's intermediate.
	checkPKI(r, globalBase, regionalBase, regionTable, pkiStore)

	// Per-region provider availability: the same set deploys into every region, so
	// each resource's provider must be declared in that region's providers block.
	if err := checkProviderAvailability(r, regionalBase, regionTable, defaults); err != nil {
		return err
	}
	// The global slice realizes against the regions.yaml global providers block.
	if global != nil {
		if err := checkGlobalProviderAvailability(r, globalBase, global, defaults); err != nil {
			return err
		}
	}

	if r.failed {
		return errors.New("validation failed")
	}
	return nil
}

func validateResourceSet(r *reporter, schemaSet map[string]*jsonschema.Schema, base string, available map[string]bool, sizeTable sizes.Table, global *globalRefs, siblingServices map[string]bool, encStore *secretstore.Store, defaults types.ProviderDefaults) error {
	networkFiles, _, err := readFolders[types.NetworkSpec](filepath.Join(base, "network"))
	if err != nil {
		return err
	}
	computeFiles, computeFolders, err := readFolders[types.ComputeSpec](filepath.Join(base, "compute"))
	if err != nil {
		return err
	}
	databaseFiles, _, err := readFolders[types.DatabaseSpec](filepath.Join(base, "database"))
	if err != nil {
		return err
	}
	serviceFiles, svcFolders, err := readFolders[types.ServiceSpec](filepath.Join(base, "service"))
	if err != nil {
		return err
	}
	pkiFiles, _, err := readFolders[types.PKIResourceSpec](filepath.Join(base, "pki"))
	if err != nil {
		return err
	}
	ingressFiles, _, err := readFolders[types.IngressSpec](filepath.Join(base, "ingress"))
	if err != nil {
		return err
	}
	appFiles, _, err := readFolders[types.AppSpec](filepath.Join(base, "app"))
	if err != nil {
		return err
	}
	gatewayFiles, _, err := readFolders[types.GatewaySpec](filepath.Join(base, "gateway"))
	if err != nil {
		return err
	}

	// Apply defaults so semantic checks see normalised specs.
	for i := range networkFiles {
		loader.NormalizeNetwork(&networkFiles[i].spec)
	}
	for i := range computeFiles {
		loader.NormalizeCompute(&computeFiles[i].spec, computeFolders[i])
	}
	for i := range databaseFiles {
		loader.NormalizeDatabase(&databaseFiles[i].spec)
	}
	for i := range pkiFiles {
		if pkiFiles[i].parseErr == nil {
			loader.NormalizePKIResource(&pkiFiles[i].spec)
		}
	}
	for i := range ingressFiles {
		if ingressFiles[i].parseErr == nil {
			loader.NormalizeIngress(&ingressFiles[i].spec)
		}
	}
	for i := range appFiles {
		if appFiles[i].parseErr == nil {
			loader.NormalizeApp(&appFiles[i].spec)
		}
	}
	for i := range gatewayFiles {
		if gatewayFiles[i].parseErr == nil {
			loader.NormalizeGateway(&gatewayFiles[i].spec)
		}
	}
	for i := range serviceFiles {
		if serviceFiles[i].parseErr != nil {
			continue
		}
		envPath := filepath.Join(svcFolders[i], "environment.yaml")
		env, err := loader.LoadEnvironmentFile(svcFolders[i])
		if err != nil {
			// Treat a malformed environment.yaml as a soft per-file failure so all
			// other services and the provider-availability pass still run.
			r.report(envPath, []string{err.Error()}, nil)
			serviceFiles[i].parseErr = err
			continue
		}
		serviceFiles[i].spec.Environment = env
		loader.NormalizeService(&serviceFiles[i].spec)
		// Validate the environment.yaml sidecar against its own schema.
		// Report even when env is nil: that means the file is present but
		// empty/comment-only — print an OK so users know it was processed.
		if env != nil {
			r.report(envPath, schemaErrors(schemaSet["environment"], env), nil)
		} else if _, statErr := os.Stat(envPath); statErr == nil {
			r.report(envPath, nil, nil)
		}
	}

	ctx := regionContext{
		available:             available,
		sizeTable:             sizeTable,
		networks:              map[string]types.NetworkSpec{},
		computeKind:           map[string]string{},
		computeCanonical:      map[string]string{},
		computeInstances:      map[string]int{},
		computeDeployer:       map[string]bool{},
		computeNames:          map[string]bool{},
		computeNetwork:        map[string]string{},
		databaseNames:         map[string]bool{},
		ingressNames:          map[string]bool{},
		ingressHost:           map[string]string{},
		appSubdomainCounts:    map[string]int{},
		portUsersByHost:       map[string]map[int][]string{},
		forwardUsersByHost:    map[string]map[int][]string{},
		targetUsersByHost:     map[string]map[int][]string{},
		udpExposedUsersByHost: map[string]map[int][]string{},
		tlsTermIngressByHost:  map[string]bool{},
		ingressHealthPort:     map[string]int{},
		ingressNamesByHost:    map[string][]string{},
		pkiResources:          map[string]string{},
		serviceAllowsGateway:  map[string]bool{},
		servicePkiByName:      map[string]string{},
		serviceNamesInScope:   map[string]bool{},
		meshServices:          map[string]bool{},
		callerCandidates:      map[string]bool{},
		forbiddenCallerNames:  map[string]bool{},
		encStore:              encStore,
		providerDefaults:      defaults,
	}
	// Gateway is a scope singleton; count the declarations so checkGateway can reject
	// a scope with more than one and checkService can reject a gateway route in a
	// scope with none.
	ctx.gatewayScopeCount = len(gatewayFiles)
	// Cross-scope caller resolution for gateway routes (ADR-0013 direction rule):
	// siblingServices are the regional mesh services, non-nil only in the global pass
	// (a global gateway route may name a regional caller — regional→global). In the
	// regional pass globalRefs.meshServices are the global services, which are the
	// FORBIDDEN callers of a regional route (a global service may only call global).
	for name := range siblingServices {
		ctx.callerCandidates[name] = true
	}
	if global != nil {
		for name := range global.meshServices {
			ctx.forbiddenCallerNames[name] = true
		}
	}
	for _, f := range networkFiles {
		ctx.networks[f.spec.Name] = f.spec
	}
	computeSpecs := make([]types.ComputeSpec, 0, len(computeFiles))
	for _, f := range computeFiles {
		computeSpecs = append(computeSpecs, f.spec)
		hasDeployer := f.spec.DeployUser != nil && f.spec.DeployUser.Name != ""
		for i := 1; i <= f.spec.InstanceCount; i++ {
			key := naming.SpecKey(f.spec.Name, i)
			ctx.computeKind[key] = f.spec.Kind
			ctx.computeInstances[key] = f.spec.InstanceCount
			ctx.computeDeployer[key] = hasDeployer
			ctx.computeNetwork[key] = f.spec.Network
		}
		ctx.computeNames[f.spec.Name] = true
		ctx.computeNetwork[f.spec.Name] = f.spec.Network
		if f.spec.InstanceCount == 1 {
			// bridge and bridge-01 both reference the same host.
			ctx.computeKind[f.spec.Name] = f.spec.Kind
			ctx.computeInstances[f.spec.Name] = f.spec.InstanceCount
		}
	}
	// Canonicalization (any compute FK form -> expanded specKey) is shared with
	// the program so validation and realization agree on host identity.
	ctx.computeCanonical = naming.CanonicalComputeKeys(computeSpecs)
	for _, f := range databaseFiles {
		ctx.databaseNames[f.spec.Name] = true
	}
	for _, f := range pkiFiles {
		if f.parseErr == nil {
			ctx.pkiResources[f.spec.Name] = f.spec.Topology
		}
	}
	for _, f := range ingressFiles {
		if f.parseErr == nil {
			ctx.ingressNames[f.spec.Name] = true
			ctx.ingressHealthPort[f.spec.Name] = f.spec.EffectiveHealthProbesPort()
			// Record the ingress's host (canonical specKey) so checkService can test
			// co-location and the same-network rule. A host FK that does not resolve
			// is left absent — checkIngress reports it.
			if hk := canonicalHost(f.spec.Host, ctx); hk != "" {
				ctx.ingressHost[f.spec.Name] = hk
				ctx.ingressNamesByHost[hk] = append(ctx.ingressNamesByHost[hk], f.spec.Name)
			}
		}
	}
	for _, f := range appFiles {
		if f.parseErr == nil {
			ctx.appSubdomainCounts[f.spec.Subdomain]++
		}
	}
	// Service name sets (this scope): every service name, and the mesh-member subset,
	// so a gateway route's allowed_services caller can be resolved and the "not a
	// service" vs "not a mesh member" distinction reported precisely.
	for _, f := range serviceFiles {
		if f.parseErr != nil {
			continue
		}
		ctx.serviceNamesInScope[f.spec.Name] = true
		ctx.servicePkiByName[f.spec.Name] = f.spec.Pki
		if f.spec.Pki != "" {
			ctx.meshServices[f.spec.Name] = true
		}
	}
	// Mesh exposure (ADR-0032): a service's mesh.port is a loopback backend bind, so it
	// shares the int-keyed backend target space with route targets, health and tcp
	// exposed ports (a collision means two binds on one port). serviceAllowsGateway
	// records whether a service lets the north-south gateway call it (mesh.allowed_services
	// contains "gateway"), so checkGateway can require a route's target service to permit
	// the gateway.
	for _, f := range serviceFiles {
		if f.parseErr != nil || f.spec.Mesh == nil {
			continue
		}
		for _, dep := range f.spec.Mesh.AllowedServices {
			if dep == gatewayCallerName {
				ctx.serviceAllowsGateway[f.spec.Name] = true
			}
		}
		backendHost := canonicalHost(f.spec.Host, ctx)
		if f.spec.Mesh.Port != 0 && backendHost != "" {
			if ctx.targetUsersByHost[backendHost] == nil {
				ctx.targetUsersByHost[backendHost] = map[int][]string{}
			}
			ctx.targetUsersByHost[backendHost][f.spec.Mesh.Port] = append(ctx.targetUsersByHost[backendHost][f.spec.Mesh.Port], f.spec.Name)
		}
	}
	// Aggregate the routes so checkService can enforce the cross-service rules. The
	// listen-port aggregations (which services use each listen port, whether a
	// tls-termination route exists) are keyed by the route's INGRESS host — that is
	// where the nginx public port lives. The target aggregation is keyed by the
	// service's own (BACKEND) host — that is where the service binds the target.
	for _, f := range serviceFiles {
		if f.parseErr != nil {
			continue
		}
		// A service is in the ingress tier when it exposes routes OR a health endpoint
		// (a health-only service still binds a backend health port that must be
		// collision-checked like a route target).
		if f.spec.Ingress == "" || (len(f.spec.Routes) == 0 && f.spec.HealthProbesPort == 0) {
			continue
		}
		ingHost, ingOK := ctx.ingressHost[f.spec.Ingress]
		backendHost := canonicalHost(f.spec.Host, ctx)
		// The backend health port is a backend bind, so it shares the target space of
		// the service's host — register it alongside route targets so a collision with
		// another service's target (or this service's own routes) is detectable.
		if f.spec.HealthProbesPort != 0 && backendHost != "" {
			if ctx.targetUsersByHost[backendHost] == nil {
				ctx.targetUsersByHost[backendHost] = map[int][]string{}
			}
			ctx.targetUsersByHost[backendHost][f.spec.HealthProbesPort] = append(ctx.targetUsersByHost[backendHost][f.spec.HealthProbesPort], f.spec.Name)
		}
		for _, rt := range f.spec.Routes {
			if ingOK {
				if ctx.portUsersByHost[ingHost] == nil {
					ctx.portUsersByHost[ingHost] = map[int][]string{}
				}
				ctx.portUsersByHost[ingHost][rt.Listen] = append(ctx.portUsersByHost[ingHost][rt.Listen], f.spec.Name)
				if rt.Type == types.IngressTypeTLSTermination {
					ctx.tlsTermIngressByHost[ingHost] = true
				}
				if rt.Type == types.IngressTypeForward {
					if ctx.forwardUsersByHost[ingHost] == nil {
						ctx.forwardUsersByHost[ingHost] = map[int][]string{}
					}
					ctx.forwardUsersByHost[ingHost][rt.Listen] = append(ctx.forwardUsersByHost[ingHost][rt.Listen], f.spec.Name)
				}
			}
			if backendHost != "" {
				if ctx.targetUsersByHost[backendHost] == nil {
					ctx.targetUsersByHost[backendHost] = map[int][]string{}
				}
				ctx.targetUsersByHost[backendHost][rt.Target] = append(ctx.targetUsersByHost[backendHost][rt.Target], f.spec.Name)
			}
		}
	}
	// Register exposed_ports separately and UNGATED: a service may declare exposed
	// ports with no ingress and no routes (a private-only service), so this cannot
	// live in the ingress-tier loop above. A tcp exposed port is a backend bind, so
	// it shares the int-keyed (implicitly-TCP) target space with route targets and
	// health ports; a udp exposed port lives in its own proto-aware space so tcp/N
	// and udp/N do not collide (ADR-0029).
	for _, f := range serviceFiles {
		if f.parseErr != nil || len(f.spec.ExposedPorts) == 0 {
			continue
		}
		backendHost := canonicalHost(f.spec.Host, ctx)
		if backendHost == "" {
			continue
		}
		for _, ep := range f.spec.ExposedPorts {
			if ep.Proto == "udp" {
				if ctx.udpExposedUsersByHost[backendHost] == nil {
					ctx.udpExposedUsersByHost[backendHost] = map[int][]string{}
				}
				ctx.udpExposedUsersByHost[backendHost][ep.Port] = append(ctx.udpExposedUsersByHost[backendHost][ep.Port], f.spec.Name)
				continue
			}
			if ctx.targetUsersByHost[backendHost] == nil {
				ctx.targetUsersByHost[backendHost] = map[int][]string{}
			}
			ctx.targetUsersByHost[backendHost][ep.Port] = append(ctx.targetUsersByHost[backendHost][ep.Port], f.spec.Name)
		}
	}
	// Seed the global slice's referenceable outputs under a `global/` prefix so a
	// regional secrets `ref:database/global/<name>` (RefName == "global/<name>")
	// resolves. Only database/compute outputs are referenceable cross-region;
	// service.host and compute.network to global are rejected explicitly below.
	if global != nil {
		for name := range global.databaseNames {
			ctx.databaseNames["global/"+name] = true
		}
		for key, kind := range global.computeKind {
			ctx.computeKind["global/"+key] = kind
		}
		for name, topology := range global.pkiTopology {
			ctx.pkiResources["global/"+name] = topology
		}
	}

	validateType(r, schemaSet["network"], networkFiles, func(s types.NetworkSpec) string { return s.Name }, func(s types.NetworkSpec) ([]string, []string) {
		return checkNetwork(s, ctx)
	})
	validateType(r, schemaSet["compute"], computeFiles, func(s types.ComputeSpec) string { return s.Name }, func(s types.ComputeSpec) ([]string, []string) {
		return checkCompute(s, ctx)
	})
	validateType(r, schemaSet["database"], databaseFiles, func(s types.DatabaseSpec) string { return s.Name }, func(s types.DatabaseSpec) ([]string, []string) {
		return checkDatabase(s, ctx)
	})
	validateType(r, schemaSet["service"], serviceFiles, func(s types.ServiceSpec) string { return s.Name }, func(s types.ServiceSpec) ([]string, []string) {
		return checkService(s, ctx)
	})
	validateType(r, schemaSet["pkiresource"], pkiFiles, func(s types.PKIResourceSpec) string { return s.Name }, func(s types.PKIResourceSpec) ([]string, []string) {
		return checkPKIResource(s)
	})
	validateType(r, schemaSet["ingress"], ingressFiles, func(s types.IngressSpec) string { return s.Name }, func(s types.IngressSpec) ([]string, []string) {
		return checkIngress(s, ctx)
	})
	validateType(r, schemaSet["app"], appFiles, func(s types.AppSpec) string { return s.Name }, func(s types.AppSpec) ([]string, []string) {
		return checkApp(s, ctx)
	})
	validateType(r, schemaSet["gateway"], gatewayFiles, func(s types.GatewaySpec) string { return s.Name }, func(s types.GatewaySpec) ([]string, []string) {
		return checkGateway(s, ctx)
	})
	return nil
}

// validateType runs schema + semantic validation over every file of one type. It
// also enforces, in this one pass that already owns each file's OK/FAIL report,
// that the resource `name:` is unique within the scope: a name keys the scope's
// FK-resolution maps, so a duplicate would silently overwrite — realizing only one
// of the colliding resources. (Reporting it here, rather than under a separate
// label, keeps a single non-contradictory line per file.) The name extractor
// pulls the comparable name out of each spec.
func validateType[T any](r *reporter, schema *jsonschema.Schema, files []fileOf[T], name func(T) string, semantic func(T) (errs, warns []string)) {
	nameCounts := map[string]int{}
	for _, f := range files {
		if f.parseErr == nil {
			nameCounts[name(f.spec)]++
		}
	}
	for _, f := range files {
		var errs, warns []string
		if f.parseErr != nil {
			r.report(f.path, []string{"parse error: " + f.parseErr.Error()}, nil)
			continue
		}
		if msgs := schemaErrors(schema, f.raw); len(msgs) > 0 {
			errs = append(errs, msgs...)
		} else {
			// Only run the name-uniqueness and semantic checks once the document is
			// structurally valid (a malformed file's name is moot — it already fails).
			if nameCounts[name(f.spec)] > 1 {
				errs = append(errs, fmt.Sprintf("name: %q is declared by more than one resource of this type in this scope; resource names must be unique within a scope", name(f.spec)))
			}
			e, w := semantic(f.spec)
			errs = append(errs, e...)
			warns = append(warns, w...)
		}
		r.report(f.path, errs, warns)
	}
}

// compileSchemas compiles every embedded JSON Schema, keyed by resource type.
func compileSchemas() (map[string]*jsonschema.Schema, error) {
	names := []string{"network", "compute", "database", "service", "environment", "pkiresource", "ingress", "app", "gateway"}
	c := jsonschema.NewCompiler()
	for _, n := range names {
		b, err := schemas.FS.ReadFile(n + ".json")
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(n+".json", bytes.NewReader(b)); err != nil {
			return nil, err
		}
	}
	out := make(map[string]*jsonschema.Schema, len(names))
	for _, n := range names {
		sch, err := c.Compile(n + ".json")
		if err != nil {
			return nil, err
		}
		out[n] = sch
	}
	return out, nil
}

// schemaErrors validates a raw document against a schema, returning a flat list
// of human-readable messages (empty if valid).
func schemaErrors(schema *jsonschema.Schema, raw any) []string {
	doc, err := toJSONDoc(raw)
	if err != nil {
		return []string{"normalize document: " + err.Error()}
	}
	if err := schema.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return flattenValidationError(ve)
		}
		return []string{err.Error()}
	}
	return nil
}

// toJSONDoc normalises a YAML-decoded value into canonical JSON types so the
// schema validator sees numbers as float64, etc.
func toJSONDoc(raw any) (any, error) {
	jb, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(jb, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// flattenValidationError turns a nested jsonschema error into leaf messages.
func flattenValidationError(ve *jsonschema.ValidationError) []string {
	if len(ve.Causes) == 0 {
		loc := ve.InstanceLocation
		if loc == "" {
			loc = "."
		}
		return []string{fmt.Sprintf("%s: %s", loc, ve.Message)}
	}
	var out []string
	for _, c := range ve.Causes {
		out = append(out, flattenValidationError(c)...)
	}
	sort.Strings(out)
	return out
}

// availableProviders returns the set of provider names defined in a region's
// provider config.
func availableProviders(providers map[string]map[string]any) map[string]bool {
	out := map[string]bool{}
	for name := range providers {
		out[name] = true
	}
	return out
}

// checkVariables validates variables.yaml, now slimmed to base_domain + ssh.
// Region selection and provider config moved to regions.yaml (see
// checkRegionsFile).
func checkVariables(r *reporter, vars types.EnvironmentVariables, path string) {
	var errs []string
	if strings.TrimSpace(vars.BaseDomain) == "" {
		errs = append(errs, "base_domain: required")
	}
	r.report(path, errs, nil)
}

// checkRegionsFile validates regions.yaml: at least one region, each with a slug
// and a non-empty providers block, and (when present) a global block carrying
// providers. Per-resource provider availability is checked against each region's
// own providers set in checkProviderAvailability.
func checkRegionsFile(r *reporter, table regions.Table, global *regions.Global, path string) {
	var errs []string
	if len(table) == 0 {
		errs = append(errs, "regions: at least one region must be defined")
	}
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ar := table[name]
		if strings.TrimSpace(ar.Slug) == "" {
			errs = append(errs, fmt.Sprintf("regions.%s: slug required", name))
		}
		if len(ar.Providers) == 0 {
			errs = append(errs, fmt.Sprintf("regions.%s: providers block required", name))
		}
		// The DNS authority is optional, but when declared both fields are
		// load-bearing: an empty provider/zone silently creates no (or zone-less)
		// records at apply. Caught here rather than failing at the DNS provider.
		if d := ar.Dns; d != nil {
			if strings.TrimSpace(d.Provider) == "" {
				errs = append(errs, fmt.Sprintf("regions.%s.dns: provider required when a dns authority is declared", name))
			}
			if strings.TrimSpace(d.Zone) == "" {
				errs = append(errs, fmt.Sprintf("regions.%s.dns: zone required when a dns authority is declared", name))
			}
		}
	}
	if global != nil {
		if len(global.Providers) == 0 {
			errs = append(errs, "global: providers block required when global is defined")
		}
		if strings.TrimSpace(global.PlacementRegion) == "" {
			errs = append(errs, "global.placementRegion: required when a global block is present")
		} else if _, err := table.Slug(global.PlacementRegion); err != nil {
			errs = append(errs, fmt.Sprintf("global.placementRegion: %q is not a defined region", global.PlacementRegion))
		}
	}
	r.report(path, errs, nil)
}

// providerErr reports a provider that is not available. A nil available set means
// provider availability is being checked separately (per region, against the
// shared resource set — see checkProviderAvailability), so the per-spec FK pass
// skips it.
// providerRef is one resource's declared provider together with its file path,
// for the per-region availability pass.
type providerRef struct {
	path     string
	provider string
}

// collectProviderRefs reads every resource file under base and returns each
// spec's path + declared provider. It only surfaces refs for files that parsed;
// malformed files are reported by the schema/FK pass in validateResourceSet.
func collectProviderRefs(base string, defaults types.ProviderDefaults) ([]providerRef, error) {
	var refs []providerRef
	if rs, err := refsOf[types.NetworkSpec](filepath.Join(base, "network"), func(s types.NetworkSpec) string {
		return types.ResolveProvider(s.Provider, "network", "", defaults)
	}); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.ComputeSpec](filepath.Join(base, "compute"), func(s types.ComputeSpec) string {
		return types.ResolveProvider(s.Provider, "compute", "", defaults)
	}); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	if rs, err := refsOf[types.DatabaseSpec](filepath.Join(base, "database"), func(s types.DatabaseSpec) string {
		return types.ResolveProvider(s.Provider, "database", s.Engine, defaults)
	}); err != nil {
		return nil, err
	} else {
		refs = append(refs, rs...)
	}
	return refs, nil
}

// refsOf reads the resource files of one type under dir and returns each parsed
// spec's path + provider (extracted via providerOf). Parse failures are skipped
// here — validateResourceSet reports them.
func refsOf[T any](dir string, providerOf func(T) string) ([]providerRef, error) {
	files, _, err := readFolders[T](dir)
	if err != nil {
		return nil, err
	}
	var refs []providerRef
	for _, f := range files {
		if f.parseErr != nil {
			continue
		}
		p := providerOf(f.spec)
		if p == "" {
			continue // empty providers caught by per-spec check; skip availability check
		}
		refs = append(refs, providerRef{path: f.path, provider: p})
	}
	return refs, nil
}

// secretsProviderNames are the providers that can serve a service's runtime
// secrets. Keep in sync with registry.SecretsProviderName and the cases in
// registry.ServiceSecretsProvisioner.
var secretsProviderNames = []string{"infisical"}

// hasSecretsProvider reports whether an available provider set includes any
// secrets-capable provider.
func hasSecretsProvider(available map[string]bool) bool {
	for _, name := range secretsProviderNames {
		if available[name] {
			return true
		}
	}
	return false
}

// servicesWithSecrets returns the path of every parsed service file under base
// that declares at least one secret. A service with any secrets needs a secrets
// provider to write them to at deploy (program.provisionServiceSecrets fails
// otherwise), regardless of the secrets' source kind.
func servicesWithSecrets(base string) ([]string, error) {
	files, folders, err := readFolders[types.ServiceSpec](filepath.Join(base, "service"))
	if err != nil {
		return nil, err
	}
	var paths []string
	for i, f := range files {
		if f.parseErr != nil {
			continue
		}
		env, err := loader.LoadEnvironmentFile(folders[i])
		if err != nil {
			return nil, err
		}
		if len(env) > 0 {
			paths = append(paths, filepath.Join(folders[i], "environment.yaml"))
		}
	}
	return paths, nil
}

// checkProviderAvailability verifies, for every region in the table, that each
// resource's declared provider is present in that region's providers block. The
// shared set deploys into every region, so a provider missing from any region is
// a failure for that region. Failures are reported under a per-region label
// rather than the resource file's path: the file's own OK/FAIL line is owned by
// the region-independent once-pass (validateResourceSet), and keying these on the
// same path would print a contradictory OK and FAIL for one file. Regions and the
// files within each are reported in sorted order for deterministic output.
func checkProviderAvailability(r *reporter, base string, table regions.Table, defaults types.ProviderDefaults) error {
	refs, err := collectProviderRefs(base, defaults)
	if err != nil {
		return err
	}
	secretSvcPaths, err := servicesWithSecrets(base)
	if err != nil {
		return err
	}
	regionNames := make([]string, 0, len(table))
	for region := range table {
		regionNames = append(regionNames, region)
	}
	sort.Strings(regionNames)

	for _, region := range regionNames {
		available := availableProviders(table[region].Providers)
		var msgs []string
		for _, ref := range refs {
			if !available[ref.provider] {
				msgs = append(msgs, fmt.Sprintf("%s: provider %q not defined in this region's regions.yaml providers block", ref.path, ref.provider))
			}
		}
		if len(secretSvcPaths) > 0 && !hasSecretsProvider(available) {
			for _, path := range secretSvcPaths {
				msgs = append(msgs, fmt.Sprintf("%s: declares secrets but this region's regions.yaml providers block has no secrets provider (one of: %s)", path, strings.Join(secretsProviderNames, ", ")))
			}
		}
		if len(msgs) > 0 {
			sort.Strings(msgs)
			r.fail(fmt.Sprintf("regions.yaml [%s] provider availability", region), msgs...)
		}
	}
	return nil
}

// checkGlobalProviderAvailability verifies each global resource's declared
// provider is present in the regions.yaml global providers block. The global
// slice is region-less, so it is checked once against the single global block
// (mirroring the per-region check in checkProviderAvailability).
// checkPKI validates each service's mesh `pki:` reference against the committed
// PKI store, credential-free (only the store's plaintext structure is read). A
// global service's leaf is minted from the PKI's global intermediate; a regional
// service's, from each region's intermediate (the regional set deploys to every
// region). Deploy (#108) cannot mint a leaf without that intermediate, so a
// missing one fails here — the "silent miss" guard.
func checkPKI(r *reporter, globalBase, regionalBase string, regionTable regions.Table, store *pki.Store) {
	regionScopes := make([]string, 0, len(regionTable))
	for name := range regionTable {
		regionScopes = append(regionScopes, name)
	}
	sort.Strings(regionScopes)

	checkServicePKI(r, globalBase, []string{pki.ScopeGlobal}, store)
	checkServicePKI(r, regionalBase, regionScopes, store)
}

// checkServicePKI applies the mesh-membership rules to every service under base,
// requiring an intermediate for each of the given scopes.
func checkServicePKI(r *reporter, base string, scopes []string, store *pki.Store) {
	services, _, err := readFolders[types.ServiceSpec](filepath.Join(base, "service"))
	if err != nil {
		r.fail(filepath.Join(base, "service"), err.Error())
		return
	}
	for i := range services {
		f := services[i]
		if f.parseErr != nil {
			continue // already reported by validateResourceSet
		}
		s := f.spec
		loader.NormalizeService(&s)
		if errs := servicePKIErrors(s, scopes, store); len(errs) > 0 {
			r.report(f.path, errs, nil)
		}
	}
	// The gateway is a mesh member too (client identity <scope>/gateway): its pki:
	// must satisfy the same membership rules for the scopes it deploys under.
	gateways, _, err := readFolders[types.GatewaySpec](filepath.Join(base, "gateway"))
	if err != nil {
		r.fail(filepath.Join(base, "gateway"), err.Error())
		return
	}
	for i := range gateways {
		f := gateways[i]
		if f.parseErr != nil {
			continue // already reported by validateResourceSet
		}
		g := f.spec
		loader.NormalizeGateway(&g)
		if errs := pkiMembershipErrors(g.Pki, scopes, store); len(errs) > 0 {
			r.report(f.path, errs, nil)
		}
	}
}

// servicePKIErrors applies the mesh-membership rules to one service for the
// given scopes and returns any validation errors. A missing required `pki:` is
// left to the JSON schema pass, so an empty value yields no error here.
func servicePKIErrors(s types.ServiceSpec, scopes []string, store *pki.Store) []string {
	return pkiMembershipErrors(s.Pki, scopes, store)
}

// pkiMembershipErrors is the credential-free mesh-membership check shared by
// services and the north-south gateway: the named PKI must exist in the store,
// be two-tier, and hold an intermediate for every scope the member deploys
// under. An empty name yields no error (the JSON schema owns required-ness).
func pkiMembershipErrors(pkiName string, scopes []string, store *pki.Store) []string {
	if pkiName == "" {
		return nil
	}
	if store == nil {
		return []string{fmt.Sprintf("pki %q references the PKI store, but %s does not exist — run `inforge pki init <env>`", pkiName, pki.FileName)}
	}
	p, ok := store.Get(pkiName)
	if !ok {
		return []string{fmt.Sprintf("pki: unknown PKI %q — known PKIs: %s", pkiName, strings.Join(store.Names(), ", "))}
	}
	if p.Topology != pki.TopologyTwoTier {
		return []string{fmt.Sprintf("pki: %q is a %s PKI; the mesh membership field must name a %s (mesh) PKI", pkiName, p.Topology, pki.TopologyTwoTier)}
	}
	var errs []string
	for _, scope := range scopes {
		if _, ok := p.Intermediates[scope]; !ok {
			errs = append(errs, fmt.Sprintf("pki %q has no intermediate for scope %q — run `inforge pki intermediate <env> %s %s`", pkiName, scope, pkiName, scope))
		}
	}
	return errs
}

func checkGlobalProviderAvailability(r *reporter, globalBase string, global *regions.Global, defaults types.ProviderDefaults) error {
	refs, err := collectProviderRefs(globalBase, defaults)
	if err != nil {
		return err
	}
	secretSvcPaths, err := servicesWithSecrets(globalBase)
	if err != nil {
		return err
	}
	available := availableProviders(global.Providers)
	var msgs []string
	for _, ref := range refs {
		if !available[ref.provider] {
			msgs = append(msgs, fmt.Sprintf("%s: provider %q not defined in regions.yaml global providers block", ref.path, ref.provider))
		}
	}
	if len(secretSvcPaths) > 0 && !hasSecretsProvider(available) {
		for _, path := range secretSvcPaths {
			msgs = append(msgs, fmt.Sprintf("%s: declares secrets but regions.yaml global providers block has no secrets provider (one of: %s)", path, strings.Join(secretsProviderNames, ", ")))
		}
	}
	if len(msgs) > 0 {
		sort.Strings(msgs)
		r.fail("regions.yaml [global] provider availability", msgs...)
	}
	return nil
}

// resolvedProviderErr returns an error when the effective provider (after applying
// defaults) is empty, or when the provider is not in the available set. It is used
// by the per-spec semantic checks; the per-region availability pass
// (checkProviderAvailability) is a separate step.
func resolvedProviderErr(specProvider, class, engine string, ctx regionContext) []string {
	effective := types.ResolveProvider(specProvider, class, engine, ctx.providerDefaults)
	if effective == "" {
		switch class {
		case "network", "compute":
			return []string{"provider: required — set provider: on this spec or add a providers.compute default in inforge.yaml"}
		case "database":
			return []string{fmt.Sprintf("provider: required — set provider: on this spec or add a providers.database.%s default in inforge.yaml", engine)}
		default:
			return []string{"provider: required"}
		}
	}
	if ctx.available != nil && !ctx.available[effective] {
		return []string{fmt.Sprintf("provider: %q not defined in this region's regions.yaml providers block", effective)}
	}
	return nil
}

func checkNetwork(s types.NetworkSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, resolvedProviderErr(s.Provider, "network", "", ctx)...)

	cidr, err := parseCIDR("cidr", s.CIDR)
	if err != nil {
		errs = append(errs, err.Error())
	}
	for i, sub := range s.Subnets {
		subnet, serr := parseCIDR(fmt.Sprintf("subnets[%d].cidr", i), sub.CIDR)
		if serr != nil {
			errs = append(errs, serr.Error())
		} else if cidr != nil && !cidrContains(cidr, subnet) {
			errs = append(errs, fmt.Sprintf("subnets[%d].cidr: %q is not within cidr %q", i, sub.CIDR, s.CIDR))
		}
	}
	return errs, warns
}

func checkCompute(s types.ComputeSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, resolvedProviderErr(s.Provider, "compute", "", ctx)...)

	if s.Kind == "cluster" {
		warns = append(warns, "kind: \"cluster\" is reserved and not implemented this phase")
	}
	// A compute attaching to a global network (network: global/<name>) is
	// recognized but rejected: materializing cross-region networking is not
	// supported yet. The global/ prefix is detected before the normal
	// network-existence check so the message is specific rather than "not found".
	if strings.HasPrefix(s.Network, "global/") {
		errs = append(errs, fmt.Sprintf("network: %q references a global network — cross-region networking is recognized but not supported yet", s.Network))
	} else if _, ok := ctx.networks[s.Network]; !ok {
		errs = append(errs, fmt.Sprintf("network: %q not found", s.Network))
	}
	if err := ctx.sizeTable.Resolve(s.Size); err != nil {
		errs = append(errs, err.Error())
	}
	if s.CloudInit != "" {
		if _, err := os.Stat(s.CloudInit); err != nil {
			errs = append(errs, fmt.Sprintf("cloud_init: file not found: %s", s.CloudInit))
		}
	}
	return errs, warns
}

func checkDatabase(s types.DatabaseSpec, ctx regionContext) (errs, warns []string) {
	errs = append(errs, resolvedProviderErr(s.Provider, "database", s.Engine, ctx)...)
	return errs, warns
}

// resolveComputeHost resolves a host: foreign key (a bare compute name) to its
// canonical specKey and validates the host is a single-instance vm in this scope.
// It is the single source of the same-scope host rule shared by service.host and
// ingress.host: noun labels the referencing resource in error messages ("a
// service" / "an ingress"). A global/ prefix is NOT handled here — callers reject
// it first, with a resource-specific message and control flow — nor is the
// deploy_user requirement (service-only). It returns the canonical specKey ("" when
// the host does not resolve) plus any host errors.
// canonicalHost resolves a compute host FK (a bare compute name) to its canonical
// specKey without emitting errors — resolveComputeHost owns the error messages.
// Returns "" when the host is not a known compute name in this scope. Used to map
// ingress.host and service.host to a comparable host identity for co-location and
// same-network checks.
func canonicalHost(host string, ctx regionContext) string {
	if _, ok := ctx.computeNames[host]; !ok {
		return ""
	}
	if c := ctx.computeCanonical[host]; c != "" {
		return c
	}
	return naming.SpecKey(host, 1)
}

func resolveComputeHost(host, noun string, ctx regionContext) (canonical string, errs []string) {
	if _, ok := ctx.computeNames[host]; !ok {
		if ctx.computeCanonical[host] != "" {
			errs = append(errs, fmt.Sprintf("host: %q is an expanded specKey; use the bare compute name instead", host))
		} else {
			errs = append(errs, fmt.Sprintf("host: %q does not resolve to a compute", host))
		}
		return "", errs
	}
	// host is a bare compute name. The canonical map has a bare-name entry only for
	// single-instance computes; for multi-instance fall back to instance 1 so the
	// specKey-keyed maps are always reachable.
	canonical = ctx.computeCanonical[host]
	if canonical == "" {
		canonical = naming.SpecKey(host, 1)
	}
	if kind := ctx.computeKind[canonical]; kind != "vm" {
		errs = append(errs, fmt.Sprintf("host: %q has kind %q; %s requires a vm host", host, kind, noun))
	}
	// The host DNS and the host's "<compute>.vm" record are derived from the bare
	// compute name (no instance index), so they cannot address one instance of a
	// multi-instance compute.
	if ctx.computeInstances[canonical] > 1 {
		errs = append(errs, fmt.Sprintf("host: %q is a multi-instance compute; %s host must be single-instance (the host DNS record cannot address one instance)", host, noun))
	}
	return canonical, errs
}

func checkService(s types.ServiceSpec, ctx regionContext) (errs, warns []string) {
	// "gateway" is the north-south gateway's mesh identity (<scope>/gateway): a
	// service with that name would mint the same leaf CN and be able to forge
	// daemon-originated traffic (a callee demuxes on X-Service-Identity ==
	// <scope>/gateway). The name is reserved.
	if s.Name == gatewayCallerName {
		errs = append(errs, fmt.Sprintf("name: %q is reserved for the north-south gateway's mesh identity (<scope>/gateway); pick another service name", gatewayCallerName))
	}
	// A service on a global host (host: global/<name>) is rejected: a service that
	// runs on a global host is defined in the global slice itself, not referenced
	// from a region. Detected before host resolution so the message is specific.
	if strings.HasPrefix(s.Host, "global/") {
		errs = append(errs, fmt.Sprintf("host: %q references a global host — a service on a global host is defined in the global slice itself, not referenced from a region", s.Host))
		return errs, warns
	}
	// reload: becomes a single ExecReload= line in the systemd unit; a newline
	// would inject additional unit directives.
	if strings.ContainsAny(s.Reload, "\n\r") {
		errs = append(errs, "reload: must be a single line (no newlines)")
	}
	canonical, hostErrs := resolveComputeHost(s.Host, "a service", ctx)
	errs = append(errs, hostErrs...)
	// deploy_user is service-only: inforge provisions the service over SSH.
	if canonical != "" && !ctx.computeDeployer[canonical] {
		errs = append(errs, fmt.Sprintf("host: %q has no deploy_user; inforge provisions the service over SSH and requires one", s.Host))
	}
	if s.Type == "container" {
		warns = append(warns, "type: \"container\" is reserved and not implemented this phase")
	}
	// Every service must declare the no-login user it runs as: the agent
	// drops privilege to this account before exec, so without it there is no
	// account to drop to. Required for secret-less and secret-bearing alike.
	if s.User == "" {
		errs = append(errs, "user: a service must declare the no-login user it runs as")
	}
	// The ingress FK: a service with routes must name an ingress resource in the
	// same scope that fronts them (ADR-0026). A global/ ingress is rejected, exactly
	// like a global host — a service fronted by a global ingress is declared in the
	// global slice itself.
	if s.Ingress != "" {
		switch {
		case strings.HasPrefix(s.Ingress, "global/"):
			errs = append(errs, fmt.Sprintf("ingress: %q references a global ingress; a service fronted by a global ingress is declared in the global slice itself, not referenced from a region", s.Ingress))
		case !ctx.ingressNames[s.Ingress]:
			errs = append(errs, fmt.Sprintf("ingress: %q does not resolve to an ingress resource in this scope", s.Ingress))
		}
	}
	// ingHost is the ingress's host ("" when the FK did not resolve); backendHost is
	// the service's own host. The maps below tolerate an empty key. Hoisted out of
	// the routes block so the health checks (a service may expose health without any
	// route) share the same co-location test.
	ingHost := ctx.ingressHost[s.Ingress]
	backendHost := canonical
	coLocated := ingHost != "" && backendHost != "" && ingHost == backendHost
	// Cross-host reachability (an ingress route's backend OR a health endpoint) rides
	// the private network, so the ingress host and the backend host must share a network
	// (same Hetzner Network — subnets within it are mutually routable). Checked once
	// for the whole ingress tier, not only for routes, so a health-only service on a
	// different network is rejected rather than silently unreachable.
	if (len(s.Routes) > 0 || s.HealthProbesPort != 0) && ingHost != "" && backendHost != "" && ingHost != backendHost {
		if ingNet, svcNet := ctx.computeNetwork[ingHost], ctx.computeNetwork[backendHost]; ingNet != svcNet {
			errs = append(errs, fmt.Sprintf("ingress: cross-host routing requires ingress %q and this service to share a network, but the ingress host is on network %q and this service's host is on %q", s.Ingress, ingNet, svcNet))
		}
	}
	if len(s.Routes) > 0 {
		if s.Ingress == "" {
			errs = append(errs, "ingress: a service with routes must name the ingress resource that fronts them")
		}
		for _, rt := range s.Routes {
			switch rt.Type {
			case types.IngressTypeTLSTermination, types.IngressTypeForward:
			default:
				errs = append(errs, fmt.Sprintf("routes: type %q is invalid; must be %q or %q", rt.Type, types.IngressTypeTLSTermination, types.IngressTypeForward))
			}
			// Both ports are mandatory and explicit (no implicit defaults).
			if rt.Listen < 1 || rt.Listen > 65535 {
				errs = append(errs, fmt.Sprintf("routes: listen %d is invalid; a public port (1..65535) is required on every route", rt.Listen))
			}
			if rt.Target < 1 || rt.Target > 65535 {
				errs = append(errs, fmt.Sprintf("routes: target for listen %d is invalid; a backend port (1..65535) is required", rt.Listen))
			}
			// The service binds rt.Target on its BACKEND host. If that host also runs
			// nginx — because it is some ingress's host (its own co-located ingress, or
			// the ingress fronting another co-located service) — nginx holds *:<listen>
			// on all interfaces there, so the target must not equal any public listen
			// port on the backend host. Keying on backendHost (not the ingress host)
			// catches the cross-host case where the backend host independently hosts an
			// ingress. When co-located and listen == target, the clearer "must differ"
			// message subsumes the collision (same port), so report only that.
			if coLocated && rt.Listen == rt.Target && rt.Listen != 0 {
				errs = append(errs, fmt.Sprintf("routes: listen and target must differ (both %d) when the service is co-located with its ingress; nginx occupies the public port on all interfaces", rt.Listen))
			} else if backendHost != "" && len(ctx.portUsersByHost[backendHost][rt.Target]) > 0 {
				errs = append(errs, fmt.Sprintf("routes: target %d collides with a public listen port on the backend host %q; nginx runs there and occupies that port on all interfaces, so the service cannot bind it", rt.Target, s.Host))
			}
			// When co-located, nginx may run internal TLS terminators on loopback ports
			// in the reserved range (mixed ssl_preread ports). A co-located backend binds
			// 127.0.0.1:<target>, so the target must stay out of that range.
			if coLocated && inReservedLoopbackRange(rt.Target) {
				errs = append(errs, fmt.Sprintf("routes: target %d falls in the reserved internal range [%d,%d) nginx uses for ssl_preread TLS terminators; pick a backend port outside it", rt.Target, nginx.LoopbackBase, nginx.LoopbackBase+nginx.MaxMixedPorts))
			}
			// A backend port is bound by one process, so two services on the same
			// backend host cannot share a target (a service may reuse its own).
			if backendHost != "" {
				if others := otherUsers(ctx.targetUsersByHost[backendHost][rt.Target], s.Name); len(others) > 0 {
					errs = append(errs, fmt.Sprintf("routes: target %d on host %q is also used by service(s) %s; a backend port belongs to a single service", rt.Target, s.Host, strings.Join(others, ", ")))
				}
			}
			if rt.Type == types.IngressTypeForward {
				// vanity is an SNI/cert concept; a forward route has no SNI.
				if len(rt.Vanity) > 0 {
					errs = append(errs, fmt.Sprintf("routes: forward on listen %d has no SNI; remove vanity", rt.Listen))
				}
				// A forward (passthrough) port is single-service-exclusive on its
				// ingress: there is exactly one map default per port, so at most one
				// forward may bind a given (ingress host, listen). It MAY coexist with
				// tls-termination routes on the same port — nginx ssl_preread routes
				// known SNIs to the terminators and the unknown SNI to this forward — so
				// exclusivity is checked against other forwards only, not all routes.
				if fwd := ctx.forwardUsersByHost[ingHost][rt.Listen]; len(fwd) > 1 {
					others := otherUsers(fwd, s.Name)
					who := "another forward route on the same service"
					if len(others) > 0 {
						who = "service(s) " + strings.Join(others, ", ")
					}
					errs = append(errs, fmt.Sprintf("routes: forward on listen %d is single-service-exclusive (one passthrough per port), but that port on ingress %q already has a forward from %s", rt.Listen, s.Ingress, who))
				}
				// ACME owns :80 on the ingress host for HTTP-01 challenges, so a
				// forward there collides with any tls-termination on the same ingress.
				if rt.Listen == 80 && ctx.tlsTermIngressByHost[ingHost] {
					errs = append(errs, fmt.Sprintf("routes: forward on :80 conflicts with a tls-termination on ingress %q (ACME owns :80 for HTTP-01 challenges)", s.Ingress))
				}
			}
		}
	}
	if s.Mesh != nil {
		errs = append(errs, checkMesh(s, backendHost, ctx)...)
	}
	// Health endpoint: the service serves health on HealthProbesPort, surfaced through
	// its ingress's public health port (Host-demuxed). A service exposing health must
	// name a resolvable ingress; the per-port collision checks only make sense once it
	// does, so they are gated behind a resolved ingress to avoid piling cascading
	// errors on top of the FK failure. (When the ingress FK is unresolved, ingHost is
	// "" and coLocated is false — the FK error already fails validation.)
	if s.HealthProbesPort != 0 {
		if s.Ingress == "" {
			errs = append(errs, "health_probes_port: a service exposing health must name the ingress resource that surfaces it")
		} else {
			// The backend health port is a distinct bind, so it must differ from every
			// one of the service's own route targets.
			for _, rt := range s.Routes {
				if rt.Target == s.HealthProbesPort {
					errs = append(errs, fmt.Sprintf("health_probes_port: %d collides with route target %d on the same service; the backend health port must differ from every route target", s.HealthProbesPort, rt.Target))
					break
				}
			}
			// Like a route target, the health port is a backend bind on the service's
			// host, so it must not equal a public listen port nginx holds there (whether
			// this host is the ingress or independently hosts one), nor another service's
			// backend target on that host.
			if backendHost != "" {
				if len(ctx.portUsersByHost[backendHost][s.HealthProbesPort]) > 0 {
					errs = append(errs, fmt.Sprintf("health_probes_port: %d collides with a public listen port on the backend host %q; nginx occupies that port on all interfaces, so the service cannot bind it for health", s.HealthProbesPort, s.Host))
				}
				if others := otherUsers(ctx.targetUsersByHost[backendHost][s.HealthProbesPort], s.Name); len(others) > 0 {
					errs = append(errs, fmt.Sprintf("health_probes_port: %d on host %q is also a backend port of service(s) %s; a backend port belongs to a single service", s.HealthProbesPort, s.Host, strings.Join(others, ", ")))
				}
			}
			// Co-located only: nginx binds the public health port (and any ssl_preread
			// loopback terminator) on the host where the service also binds its backend
			// health port, so those must not clash. Cross-host backends bind on another
			// host, so no clash is possible there.
			if coLocated {
				if healthIngressPort := ctx.ingressHealthPort[s.Ingress]; healthIngressPort != 0 && s.HealthProbesPort == healthIngressPort {
					errs = append(errs, fmt.Sprintf("health_probes_port: %d equals ingress %q's public health port; when co-located the backend health port must differ (nginx binds the public port on all interfaces)", s.HealthProbesPort, s.Ingress))
				}
				if inReservedLoopbackRange(s.HealthProbesPort) {
					errs = append(errs, fmt.Sprintf("health_probes_port: %d falls in the reserved internal range [%d,%d) nginx uses for ssl_preread TLS terminators; pick a port outside it", s.HealthProbesPort, nginx.LoopbackBase, nginx.LoopbackBase+nginx.MaxMixedPorts))
				}
			}
		}
	}
	// exposed_ports (ADR-0029): each is a private-network backend bind inforge opens
	// only to the host's private CIDR. Unlike health, a service MAY declare them with
	// no ingress (a private-only service), so there is no ingress requirement. The
	// collision model is proto-aware: a tcp exposed port shares the backend TCP space
	// with route targets and health ports; a udp exposed port collides only with
	// another udp exposed port.
	if len(s.ExposedPorts) > 0 {
		hostRunsIngress := backendHost != "" && len(ctx.ingressNamesByHost[backendHost]) > 0
		seen := map[types.ExposedPort]bool{}
		for _, ep := range s.ExposedPorts {
			if ep.Proto != "tcp" && ep.Proto != "udp" {
				errs = append(errs, fmt.Sprintf("exposed_ports: proto %q is invalid (want tcp or udp)", ep.Proto))
				continue
			}
			if seen[ep] {
				errs = append(errs, fmt.Sprintf("exposed_ports: %s/%d is declared more than once on this service", ep.Proto, ep.Port))
				continue
			}
			seen[ep] = true
			if ep.Proto == "udp" {
				// A udp exposed port collides only with another service's udp exposed
				// port on this host (tcp/N and udp/N are distinct OS binds).
				if backendHost != "" {
					if others := otherUsers(ctx.udpExposedUsersByHost[backendHost][ep.Port], s.Name); len(others) > 0 {
						errs = append(errs, fmt.Sprintf("exposed_ports: udp/%d on host %q is also exposed by service(s) %s; a private backend port belongs to a single service", ep.Port, s.Host, strings.Join(others, ", ")))
					}
				}
				continue
			}
			// A tcp exposed port is a backend bind: it must differ from this service's
			// own route targets and health port.
			for _, rt := range s.Routes {
				if rt.Target == ep.Port {
					errs = append(errs, fmt.Sprintf("exposed_ports: tcp/%d collides with route target %d on the same service; an exposed backend port must differ from every route target", ep.Port, rt.Target))
					break
				}
			}
			if s.HealthProbesPort != 0 && s.HealthProbesPort == ep.Port {
				errs = append(errs, fmt.Sprintf("exposed_ports: tcp/%d collides with this service's health_probes_port; the two are distinct backend binds", ep.Port))
			}
			if backendHost != "" {
				// It must not equal a public listen port nginx holds on this host, nor
				// another service's tcp backend port (route target / health / exposed).
				if len(ctx.portUsersByHost[backendHost][ep.Port]) > 0 {
					errs = append(errs, fmt.Sprintf("exposed_ports: tcp/%d collides with a public listen port on host %q; nginx occupies that port on all interfaces, so the service cannot bind it", ep.Port, s.Host))
				}
				if others := otherUsers(ctx.targetUsersByHost[backendHost][ep.Port], s.Name); len(others) > 0 {
					errs = append(errs, fmt.Sprintf("exposed_ports: tcp/%d on host %q is also a backend port of service(s) %s; a backend port belongs to a single service", ep.Port, s.Host, strings.Join(others, ", ")))
				}
			}
			// When this host runs an ingress, nginx may bind a loopback ssl_preread
			// terminator in the reserved range; a co-located tcp bind there clashes.
			if hostRunsIngress && inReservedLoopbackRange(ep.Port) {
				errs = append(errs, fmt.Sprintf("exposed_ports: tcp/%d falls in the reserved internal range [%d,%d) nginx uses for ssl_preread TLS terminators on this ingress host; pick a port outside it", ep.Port, nginx.LoopbackBase, nginx.LoopbackBase+nginx.MaxMixedPorts))
			}
		}
	}
	// Validate the environment contract: key namespace, source DSL, vault store
	// presence. Provider is not checked here — it is derived from the region config
	// and validated as part of provider availability checks, not per-entry.
	envKeys := make([]string, 0, len(s.Environment))
	for k := range s.Environment {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		errs = append(errs, reservedEnvNameErrs(k, "environment."+k)...)
		parsed, err := ParseSource(s.Environment[k])
		if err != nil {
			errs = append(errs, fmt.Sprintf("environment.%s: %s", k, err.Error()))
			continue
		}
		if parsed.Kind == SourceVault {
			// A vault source's ciphertext must already exist in the committed store —
			// fail at validate time so the fix is cheap (run inforge secret set, commit).
			if ctx.encStore == nil {
				errs = append(errs, fmt.Sprintf("environment.%s: source is vault but resources/<env>/%s does not exist — run `inforge secret init <env>`, then `inforge secret set <env> %s %s`", k, secretstore.FileName, s.Name, parsed.VaultKey))
			} else if _, ok := ctx.encStore.Get(s.Container, parsed.VaultKey); !ok {
				errs = append(errs, fmt.Sprintf("environment.%s: no ciphertext for key %q in container %q in %s — run `inforge secret set <env> %s %s` and commit", k, parsed.VaultKey, s.Container, secretstore.FileName, s.Name, parsed.VaultKey))
			}
			continue
		}
		if parsed.Kind != SourceRef {
			continue
		}
		switch parsed.RefType {
		case "database":
			// A database exposes no referenceable outputs (ADR-0025): the
			// credential-bearing connectionUrl was removed so the admin credential is
			// never handed to a consumer. DB credentials flow only through a grant.
			errs = append(errs, fmt.Sprintf("environment.%s: a database exposes no referenceable outputs; use a grants: entry for DB credentials (ADR-0025)", k))
		case "compute":
			if parsed.RefOutput != "publicIp" {
				errs = append(errs, fmt.Sprintf("environment.%s: unknown compute output %q (want publicIp)", k, parsed.RefOutput))
			}
			if _, ok := ctx.computeKind[parsed.RefName]; !ok {
				errs = append(errs, fmt.Sprintf("environment.%s: compute %q does not resolve to a compute instance", k, parsed.RefName))
			}
		}
	}
	errs = append(errs, checkGrants(s, ctx)...)
	return errs, warns
}

// checkPKIResource validates a PKI resource manifest's own shape (credential-free):
// a PKI resource is the root-only standalone CA grants target, so a non-root-only
// topology is rejected — the two-tier mesh PKI lives in pki.enc.yaml and is
// consumed via the service pki: field, never as a grant target.
func checkPKIResource(s types.PKIResourceSpec) (errs, warns []string) {
	if s.Topology != pki.TopologyRootOnly {
		errs = append(errs, fmt.Sprintf("topology: %q is invalid for a PKI resource; it must be %q (the two-tier mesh PKI lives in %s and is consumed via the service pki: field)", s.Topology, pki.TopologyRootOnly, pki.FileName))
	}
	return errs, warns
}

// checkIngress validates an ingress resource: its host: foreign key must resolve
// to a single-instance vm compute in the SAME scope. A global/ prefix is rejected
// explicitly — an ingress on a global host is declared in the global slice
// itself, not referenced from a region (mirroring service.host). The ingress
// carries no provider; it inherits its host's. Its name must be unique among the
// scope's ingress resources.
func checkIngress(s types.IngressSpec, ctx regionContext) (errs, warns []string) {
	// An ingress on a global host (host: global/<name>) is rejected: it is declared
	// in the global slice itself, not referenced from a region. Detected before host
	// resolution so the message is specific (mirrors checkService).
	if strings.HasPrefix(s.Host, "global/") {
		errs = append(errs, fmt.Sprintf("host: %q references a global compute; an ingress on a global host is declared in the global slice itself, not referenced from a region", s.Host))
		return errs, warns
	}
	hostKey, hostErrs := resolveComputeHost(s.Host, "an ingress", ctx)
	errs = append(errs, hostErrs...)

	// One ingress per host: the nginx config, firewall, and health port are derived
	// per host, so two ingresses sharing a host would silently merge or override each
	// other (e.g. only one health port survives). Reject the collision.
	if hostKey != "" {
		if others := otherUsers(ctx.ingressNamesByHost[hostKey], s.Name); len(others) > 0 {
			errs = append(errs, fmt.Sprintf("host: %q is already used by ingress %s; a compute host hosts at most one ingress (the derived nginx config and firewall are per-host)", s.Host, strings.Join(others, ", ")))
		}
	}

	// The public health port nginx exposes (health_probes_port, default 81) must not
	// collide with anything else nginx binds on this host: :80 (ACME HTTP-01) or any
	// service route's public listen port. The backend health port is validated on the
	// service (checkService), since that is where the service binds it.
	healthPort := s.EffectiveHealthProbesPort()
	if healthPort == 80 {
		errs = append(errs, "health_probes_port: must not be 80 (ACME HTTP-01 owns :80 on the ingress host)")
	}
	// nginx may run internal ssl_preread TLS terminators on loopback ports in the
	// reserved range when a port is mixed; the public health server would bind the
	// same port number, so keep the health port out of that range.
	if inReservedLoopbackRange(healthPort) {
		errs = append(errs, fmt.Sprintf("health_probes_port: %d falls in the reserved internal range [%d,%d) nginx uses for ssl_preread TLS terminators; pick a health port outside it", healthPort, nginx.LoopbackBase, nginx.LoopbackBase+nginx.MaxMixedPorts))
	}
	if hostKey != "" {
		if users := ctx.portUsersByHost[hostKey][healthPort]; len(users) > 0 {
			errs = append(errs, fmt.Sprintf("health_probes_port: %d collides with a route listen port on this ingress host (used by service(s) %s); pick a distinct health port", healthPort, strings.Join(users, ", ")))
		}
	}
	return errs, warns
}

// inReservedLoopbackRange reports whether a backend port falls in the range nginx
// reserves for internal ssl_preread TLS terminators on an ingress host. A co-located
// backend binding such a port would clash with a loopback terminator.
func inReservedLoopbackRange(port int) bool {
	return port >= nginx.LoopbackBase && port < nginx.LoopbackBase+nginx.MaxMixedPorts
}

// checkApp validates an app (front-end) resource: its ingress: foreign key must
// resolve to an ingress resource in the SAME scope. A global/ prefix is rejected
// explicitly — an app served from a global ingress is declared in the global
// slice itself, not referenced from a region (mirroring service.host, which is a
// same-scope compute name and never a global/ reference). The app declares no
// provider; it inherits the ingress's (its compute host's). Its name and its
// subdomain must each be unique within the scope.
func checkApp(s types.AppSpec, ctx regionContext) (errs, warns []string) {
	if s.Subdomain != "" && ctx.appSubdomainCounts[s.Subdomain] > 1 {
		errs = append(errs, fmt.Sprintf("subdomain: %q is used by more than one app in this scope; each app must map to a distinct public FQDN", s.Subdomain))
	}
	switch {
	case strings.HasPrefix(s.Ingress, "global/"):
		errs = append(errs, fmt.Sprintf("ingress: %q references a global ingress; an app served from a global ingress is declared in the global slice itself, not referenced from a region", s.Ingress))
	case !ctx.ingressNames[s.Ingress]:
		errs = append(errs, fmt.Sprintf("ingress: %q does not resolve to an ingress resource in this scope", s.Ingress))
	}
	return errs, warns
}

// gatewayCallerName is the reserved allow-list token naming the north-south gateway's
// mesh identity (<scope>/gateway). A service lists it in mesh.allowed_services to be
// reachable by daemon traffic routed through the gateway (ADR-0032).
const gatewayCallerName = "gateway"

// checkGateway validates the north-south daemon gateway resource (ADR-0032): its host:
// foreign key must resolve to a single-instance vm compute in the SAME scope — a
// global/ prefix is rejected explicitly, exactly like service.host/ingress.host. A
// scope may declare at most one gateway (a scope singleton — one public daemon edge per
// scope). Each route's path must be a non-root prefix, unique within the gateway, and
// its target service must resolve in this scope AND permit the gateway (list "gateway"
// in its mesh.allowed_services — the two sides must agree). The gateway carries no
// provider (it inherits its host's); its name uniqueness is enforced in validateType.
func checkGateway(s types.GatewaySpec, ctx regionContext) (errs, warns []string) {
	if strings.HasPrefix(s.Host, "global/") {
		errs = append(errs, fmt.Sprintf("host: %q references a global compute; a gateway on a global host is declared in the global slice itself, not referenced from a region", s.Host))
		return errs, warns
	}
	if _, hostErrs := resolveComputeHost(s.Host, "a gateway", ctx); len(hostErrs) > 0 {
		errs = append(errs, hostErrs...)
	}
	if ctx.gatewayScopeCount > 1 {
		errs = append(errs, "a scope may declare at most one gateway (it is a scope singleton — one public daemon edge per scope)")
	}
	seenPath := map[string]bool{}
	for _, rt := range s.Routes {
		// Path: the loader normalized it to "/<p>/"; a "/" means the authored value was
		// empty or slash-only. A route must claim a non-root prefix (a "/" would capture
		// every request), be a plain prefix, and be unique within the gateway.
		switch {
		case rt.Path == "" || rt.Path == "/":
			errs = append(errs, "routes: a gateway route must declare a non-root path prefix (e.g. /ddns/)")
		case strings.Contains(rt.Path, "..") || strings.ContainsAny(rt.Path, " \t"):
			errs = append(errs, fmt.Sprintf("routes: gateway path %q is invalid; use a plain URL prefix like /ddns/ (no spaces or \"..\")", rt.Path))
		case seenPath[rt.Path]:
			errs = append(errs, fmt.Sprintf("routes: gateway path %q is declared more than once on this gateway", rt.Path))
		default:
			seenPath[rt.Path] = true
		}
		// Target service: must resolve to a service in this scope, and that service must
		// permit the gateway (mesh.allowed_services contains "gateway"). The gateway
		// reaches it through the mesh, so a service with no mesh block / no gateway in its
		// allow list is unreachable — reject it here so the two sides can't drift.
		switch {
		case rt.Service == "":
			errs = append(errs, fmt.Sprintf("routes: gateway path %q has no service", rt.Path))
		case !ctx.serviceNamesInScope[rt.Service]:
			errs = append(errs, fmt.Sprintf("routes: gateway path %q routes to service %q, which does not resolve to a service in this scope", rt.Path, rt.Service))
		case !ctx.serviceAllowsGateway[rt.Service]:
			errs = append(errs, fmt.Sprintf("routes: gateway path %q routes to service %q, but that service does not permit the gateway — add %q to its mesh.allowed_services", rt.Path, rt.Service, gatewayCallerName))
		case s.Pki != "" && ctx.servicePkiByName[rt.Service] != s.Pki:
			// The gateway's client leaf chains to ITS pki's intermediates; a callee
			// verifies callers against its OWN mesh's trust set. Different meshes can
			// never complete the handshake, so reject the drift at authoring time.
			errs = append(errs, fmt.Sprintf("routes: gateway path %q routes to service %q, which joins mesh %q while the gateway joins %q — a gateway and its route targets must share one pki", rt.Path, rt.Service, ctx.servicePkiByName[rt.Service], s.Pki))
		}
	}
	// The gateway's FQDN lives in the same flat public namespace as app FQDNs
	// (<subdomain>[.<slug>].<base>) — a subdomain an app already claims would
	// collide on the DNS record and the ACME cert.
	if s.Subdomain != "" && ctx.appSubdomainCounts[s.Subdomain] > 0 {
		errs = append(errs, fmt.Sprintf("subdomain: %q is already used by an app in this scope; the gateway needs its own public FQDN", s.Subdomain))
	}
	return errs, warns
}

// checkMesh validates a service's mesh: block (ADR-0032): the loopback Port it serves
// peers on (a backend bind, collision-checked like a route target against the service's
// own binds, other services on the host, and any public listen port nginx holds there),
// and its callee-side allowed_services (each a mesh member in a permitted direction, or
// the reserved "gateway" token). backendHost is the service's canonical host key ("" when
// the host FK did not resolve).
func checkMesh(s types.ServiceSpec, backendHost string, ctx regionContext) []string {
	var errs []string
	m := s.Mesh
	// A mesh member must have a pki: identity (its leaf is what peers verify).
	if s.Pki == "" {
		errs = append(errs, "mesh: a service with a mesh block must declare pki: (its mesh identity)")
	}
	if m.Port < 1 || m.Port > 65535 {
		errs = append(errs, fmt.Sprintf("mesh.port: %d is invalid; a backend port (1..65535) is required", m.Port))
	} else {
		// The mesh port is a loopback backend bind: distinct from this service's route
		// targets, health port and tcp exposed ports, and from another service's backend
		// port or a public listen port nginx holds on this host.
		for _, rt := range s.Routes {
			if rt.Target == m.Port {
				errs = append(errs, fmt.Sprintf("mesh.port: %d collides with route target %d on the same service; distinct backend binds", m.Port, rt.Target))
				break
			}
		}
		if s.HealthProbesPort == m.Port {
			errs = append(errs, fmt.Sprintf("mesh.port: %d collides with this service's health_probes_port; distinct backend binds", m.Port))
		}
		for _, ep := range s.ExposedPorts {
			if ep.Proto == "tcp" && ep.Port == m.Port {
				errs = append(errs, fmt.Sprintf("mesh.port: %d collides with this service's tcp exposed_port; distinct backend binds", m.Port))
				break
			}
		}
		if backendHost != "" {
			if len(ctx.portUsersByHost[backendHost][m.Port]) > 0 {
				errs = append(errs, fmt.Sprintf("mesh.port: %d collides with a public listen port on host %q; nginx occupies that port on all interfaces", m.Port, s.Host))
			}
			if others := otherUsers(ctx.targetUsersByHost[backendHost][m.Port], s.Name); len(others) > 0 {
				errs = append(errs, fmt.Sprintf("mesh.port: %d on host %q is also a backend port of service(s) %s; a backend port belongs to a single service", m.Port, s.Host, strings.Join(others, ", ")))
			}
		}
	}
	// allowed_services: each bare name must resolve to a mesh member permitted to call
	// this service, or the reserved "gateway" token. Same-scope mesh services qualify; a
	// cross-scope caller qualifies only regional→global (callerCandidates).
	// forbiddenCallerNames names an existing cross-scope service in the wrong direction.
	seenDep := map[string]bool{}
	for _, dep := range m.AllowedServices {
		switch {
		case dep == "":
			errs = append(errs, "mesh.allowed_services: has an empty entry")
		case dep == gatewayCallerName:
			// the north-south gateway identity — always a valid caller token
		case dep == s.Name:
			errs = append(errs, fmt.Sprintf("mesh.allowed_services: lists its own service %q; a service does not call itself", dep))
		case seenDep[dep]:
			errs = append(errs, fmt.Sprintf("mesh.allowed_services: lists service %q more than once", dep))
		case ctx.meshServices[dep] || ctx.callerCandidates[dep]:
			// resolves to a mesh member in a permitted direction
		case ctx.forbiddenCallerNames[dep]:
			errs = append(errs, fmt.Sprintf("mesh.allowed_services: allows %q, a global service — a global service may only call other global services (regional→global only), so it cannot call this service", dep))
		case ctx.serviceNamesInScope[dep]:
			errs = append(errs, fmt.Sprintf("mesh.allowed_services: allows %q, which is not a mesh member; a caller must declare pki: to have a mesh identity", dep))
		default:
			errs = append(errs, fmt.Sprintf("mesh.allowed_services: allows %q, which does not resolve to a service that may call this one in this scope", dep))
		}
		seenDep[dep] = true
	}
	return errs
}

// reservedEnvNameErrs returns errors for an env-var name that collides with a
// namespace inforge owns: the INFORGE_* prefix, or one of the mesh cert-path names
// inforge projects and injects (meshcert.DescriptorFiles()) — a service mapping a
// value onto either would silently collide with an inforge-injected value. Shared
// by the environment.yaml check and the grant-outputs check so the reserved set is
// enforced identically in both; label prefixes the message (e.g. "environment.FOO"
// or "grants[0].outputs.FOO").
func reservedEnvNameErrs(envName, label string) []string {
	var errs []string
	if strings.HasPrefix(envName, agent.ReservedEnvPrefix) {
		errs = append(errs, fmt.Sprintf("%s: env var name uses the reserved %s* namespace owned by inforge", label, agent.ReservedEnvPrefix))
	}
	if _, reserved := meshcert.DescriptorFiles()[envName]; reserved {
		errs = append(errs, fmt.Sprintf("%s: env var name is reserved by inforge for mesh certificate paths", label))
	}
	return errs
}

// checkGrants validates a service's grants: entries credential-free (ADR-0025):
// the target resolves to a supported Grantable of the right shape, the permission
// is ro/rw, every outputs template interpolates at least one {FIELD} that the
// target publishes for that permission, a file field stands alone (it is a path,
// not a substring), and each output env-var name avoids the reserved INFORGE_*
// namespace and the mesh cert-path names (meshcert.DescriptorFiles()) and does not
// collide with the service's environment.yaml keys or another grant's outputs. The
// cross-region boundary falls out of target resolution: the shared regional set +
// the global/ prefix mean a regional service can only name its own region's
// resources or a global/<name> one (mirroring ref:).
func checkGrants(s types.ServiceSpec, ctx regionContext) []string {
	var errs []string
	// grantEnvNames tracks each output name across all of the service's grants so a
	// name defined by two grants is reported (the descriptor merges them into one
	// env namespace alongside environment.yaml).
	grantEnvNames := map[string]string{}
	// seenTargets rejects two grants on the same resource: each grant mints a
	// resource (e.g. a per-service DB role named for the (service, target) pair), so
	// a duplicate target would collide on one provider resource at deploy. One grant
	// per target — rw already subsumes ro.
	seenTargets := map[string]bool{}
	for gi := range s.Grants {
		g := s.Grants[gi]
		label := fmt.Sprintf("grants[%d]", gi)

		typ, name, ok := strings.Cut(g.Resource, "/")
		if !ok || typ == "" || name == "" {
			errs = append(errs, fmt.Sprintf("%s.resource: %q must be \"<type>/<name>\" (e.g. database/main)", label, g.Resource))
			continue
		}
		if seenTargets[g.Resource] {
			errs = append(errs, fmt.Sprintf("%s.resource: %q is granted more than once; declare a single grant per resource", label, g.Resource))
			continue
		}
		seenTargets[g.Resource] = true
		grantable, supported := grant.For(typ)
		if !supported {
			errs = append(errs, fmt.Sprintf("%s.resource: %q is not a grantable type (want database/* or pki/*)", label, g.Resource))
			continue
		}
		perm := grant.Permission(g.Permission)
		if !perm.Valid() {
			errs = append(errs, fmt.Sprintf("%s.permission: %q is invalid; must be %q or %q", label, g.Permission, grant.PermissionRO, grant.PermissionRW))
			continue
		}

		// Resolve the target: existence (which also enforces the cross-region
		// boundary) plus the type-specific shape. A target that does not resolve
		// skips the field-publication checks below, so a wrong resource name yields
		// one clear error instead of a misleading "field not published" cascade.
		targetResolved := false
		switch typ {
		case "database":
			if ctx.databaseNames[name] {
				targetResolved = true
			} else {
				errs = append(errs, fmt.Sprintf("%s.resource: database %q not found", label, name))
			}
		case "pki":
			topology, found := ctx.pkiResources[name]
			switch {
			case !found:
				errs = append(errs, fmt.Sprintf("%s.resource: pki resource %q not found", label, name))
			case topology != pki.TopologyRootOnly:
				errs = append(errs, fmt.Sprintf("%s.resource: pki resource %q has topology %q; a grant target must be %q", label, name, topology, pki.TopologyRootOnly))
			default:
				targetResolved = true
			}
		default:
			// grant.For accepted this type but checkGrants has no resolver for it:
			// a new Grantable was wired into grant.For without adding its target
			// resolution here. Fail loudly rather than silently skip validation.
			errs = append(errs, fmt.Sprintf("%s.resource: grantable type %q has no validation support — add a target resolver in checkGrants", label, typ))
			continue
		}

		// The fields the target publishes for this permission (credential-free).
		values, files := grantable.FieldNames(perm)
		published := map[string]bool{}
		isFile := map[string]bool{}
		for _, v := range values {
			published[v] = true
		}
		for _, f := range files {
			published[f] = true
			isFile[f] = true
		}

		outNames := make([]string, 0, len(g.Outputs))
		for k := range g.Outputs {
			outNames = append(outNames, k)
		}
		sort.Strings(outNames)
		for _, envName := range outNames {
			tmpl := g.Outputs[envName]
			errs = append(errs, reservedEnvNameErrs(envName, label+".outputs."+envName)...)
			if _, clash := s.Environment[envName]; clash {
				errs = append(errs, fmt.Sprintf("%s.outputs.%s: env var collides with a key in the service's environment.yaml", label, envName))
			}
			if prev, clash := grantEnvNames[envName]; clash {
				errs = append(errs, fmt.Sprintf("%s.outputs.%s: env var is also produced by the grant on %q", label, envName, prev))
			} else {
				grantEnvNames[envName] = g.Resource
			}

			t, perr := grant.ParseTemplate(tmpl)
			if perr != nil {
				errs = append(errs, fmt.Sprintf("%s.outputs.%s: %s", label, envName, perr.Error()))
				continue
			}
			fields := t.Fields()
			// A grant output materializes credential fields: a template that
			// interpolates none is almost always a dropped-brace typo (a constant
			// belongs in environment.yaml as a literal, not a grant output).
			if len(fields) == 0 {
				errs = append(errs, fmt.Sprintf("%s.outputs.%s: template %q interpolates no field; a grant output must reference at least one {FIELD}", label, envName, tmpl))
				continue
			}
			// Field publication is relative to the target's published set, so only
			// check it once the target resolved (an unresolved target already has its
			// own error above).
			if !targetResolved {
				continue
			}
			hasFile := false
			for _, f := range fields {
				if !published[f] {
					errs = append(errs, fmt.Sprintf("%s.outputs.%s: field {%s} is not published by %s for permission %q (published: %s)", label, envName, f, g.Resource, perm, publishedFields(values, files)))
					continue
				}
				if isFile[f] {
					hasFile = true
				}
			}
			// A file field is an on-host path, not a substring: its template must be
			// exactly that one placeholder, with no other tokens and no literal text.
			if hasFile && (len(fields) != 1 || t.HasLiteral()) {
				errs = append(errs, fmt.Sprintf("%s.outputs.%s: a file field must stand alone (it is a path, not a substring); template %q mixes it with other tokens", label, envName, tmpl))
			}
		}
	}
	return errs
}

// publishedFields renders the value and file fields a grant target publishes, for
// an error message. Files are suffixed so a reader sees which fields are paths.
func publishedFields(values, files []string) string {
	parts := make([]string, 0, len(values)+len(files))
	parts = append(parts, values...)
	for _, f := range files {
		parts = append(parts, f+" (file)")
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// otherUsers returns the port users that are not self, sorted and de-duplicated,
// for a stable, self-excluding conflict message.
func otherUsers(users []string, self string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range users {
		if u == self || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}
