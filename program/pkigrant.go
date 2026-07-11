package program

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/wardnet/inforge/internal/grant"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/types"
)

// The two grantable resource-type tokens (grant.For). A database grant publishes
// value fields (composed into env secrets, resolveDatabaseGrants); a pki grant
// publishes file fields (projected as on-host PEMs, resolvePKIGrants). Every
// grant must be materialized by exactly one of the two.
const (
	grantTypeDatabase = "database"
	grantTypePKI      = "pki"
)

// pkiGrantTarget resolves a grant's resource name ("<name>" or "global/<name>") to
// the bare key the env-scoped PKI store is keyed by. The store is one file per
// environment (region-independent), so the cross-scope prefix carries no lookup
// meaning here and is stripped — unlike the region-keyed Compute/Database maps,
// where types.ResolveScoped redirects on it. Both spellings come from
// types.GlobalPrefix so the validator and the deploy can never disagree.
func pkiGrantTarget(name string) string {
	return types.StripGlobalPrefix(name)
}

// pkiGrantScope is the scope a grant's target must serve: a regional service may
// only reach a PKI scoped to its own region, or a global one via "global/<name>".
func pkiGrantScope(resourceName, region string) string {
	if strings.HasPrefix(resourceName, types.GlobalPrefix) || region == globalScope {
		return pki.ScopeGlobal
	}
	return region
}

// decryptPKIGrantMaterial resolves the material of every root-only PKI resource
// some service actually grants, once per deploy (ADR-0025 slice C). It collects
// the pki/* grant targets across the regional and global service specs, loads the
// environment's committed store (resources/<env>/pki.enc.yaml), and decrypts each
// PKI's root signing key with the INFORGE_SECRETS_KEY master identity — a
// root-only root key is encrypted to the CI recipient precisely so the deploy can
// deliver it to an online issuer (pki.Store.RootKeyRecipient).
//
// The result feeds types.AllOutputs.PKI, the map resolvePKIGrants serves grants
// from. It mirrors decryptEncryptedSecrets: decryption happens once, here,
// provider-neutrally, and returns (nil, nil) when no service declares a pki/*
// grant — so an environment that grants no PKI never needs the store or the key.
// On a keyless dry run the key is a placeholder; a real up without the key fails.
//
// A granted PKI that is absent from the store is a hard error. `inforge validate`
// rejects it too, but this is the deploy's own last gate: silently skipping it
// would write a descriptor missing the grant's env vars, and the service would
// crash-loop at startup on a required variable that no longer exists.
func decryptPKIGrantMaterial(res, globalRes types.Resources, dir, env string, dryRun bool) (map[string]types.PKIMaterial, error) {
	wanted := map[string]bool{}
	for _, set := range []types.Resources{res, globalRes} {
		for _, svc := range set.Service {
			for _, g := range svc.Grants {
				typ, name, ok := strings.Cut(g.Resource, "/")
				if !ok || typ != grantTypePKI {
					continue
				}
				wanted[pkiGrantTarget(name)] = true
			}
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	storePath := pki.Path(dir, env)
	store, err := pki.Load(storePath)
	if err != nil {
		if errors.Is(err, pki.ErrNotFound) {
			return nil, fmt.Errorf("environment %q declares a pki/* grant but %s does not exist — create the PKI with `inforge pki add %s <name> --topology root-only` and commit the store", env, storePath, env)
		}
		return nil, err
	}

	identity, err := masterIdentity(dryRun)
	if err != nil {
		return nil, err
	}

	out := make(map[string]types.PKIMaterial, len(wanted))
	for _, name := range sortedKeys(wanted) {
		p, ok := store.Get(name)
		if !ok {
			return nil, fmt.Errorf("pki grant target %q has no material in %s — the PKI resource is declared but was never generated; run `inforge pki add %s %s --topology root-only` and commit the store", name, storePath, env, name)
		}
		// A grant target must be root-only: a two-tier root key is encrypted to the
		// OFFLINE root recipient, so CI could not decrypt it here even if the shape
		// were otherwise acceptable. validate.checkGrants rejects this too.
		if p.Topology != pki.TopologyRootOnly {
			return nil, fmt.Errorf("pki grant target %q has topology %q; a grant target must be %q", name, p.Topology, pki.TopologyRootOnly)
		}
		key, err := decodeValue(p.Root.Key, identity)
		if err != nil {
			return nil, fmt.Errorf("decrypt root signing key for pki %q: %w", name, err)
		}
		out[name] = types.PKIMaterial{Cert: p.Root.Cert, Key: key, Scope: p.Scope}
	}
	return out, nil
}

// resolvePKIGrants materializes a service's pki/* grants into the two halves a
// file-field grant delivers (ADR-0025):
//
//   - descriptorFiles (env-var -> file key): the descriptor's files: map, fully
//     known at plan time — it names only keys, never PEM content.
//   - blobFiles (file key -> PEM): the material deliverServiceSecrets age-encrypts
//     into the host's secrets.age.
//
// The agent joins the two: for each descriptor files: entry it looks the key up in
// the decrypted blob, writes the PEM into the service's tmpfs RuntimeDir, and sets
// the env var to that path — so the service reads a PATH, and the PEM never lands
// in its environment or the journal. Both sides derive the key from grant.FileKey,
// so they cannot drift.
//
// Database grants are materialized separately (resolveDatabaseGrants): their
// fields are value fields composed into env secrets, a disjoint mechanism. A grant
// of any other type is rejected here rather than skipped — an unmaterialized grant
// must never reach a host as a silently-missing env var.
func resolvePKIGrants(ctx *pulumi.Context, svc types.ServiceSpec, all types.AllOutputs, env, region string) (descriptorFiles map[string]string, blobFiles map[string]pulumi.StringOutput, err error) {
	if len(svc.Grants) == 0 {
		return nil, nil, nil
	}
	descriptorFiles = map[string]string{}
	blobFiles = map[string]pulumi.StringOutput{}
	for _, g := range svc.Grants {
		typ, name, ok := strings.Cut(g.Resource, "/")
		if !ok {
			return nil, nil, fmt.Errorf("grant %q: not a \"<type>/<name>\" reference", g.Resource)
		}
		switch typ {
		case grantTypePKI:
			// handled below
		case grantTypeDatabase:
			continue // value fields — resolveDatabaseGrants owns these
		default:
			// grant.For accepted the type (validate ran) but no resolver materializes
			// it. Fail loudly: a silently-skipped grant ships a descriptor missing the
			// env vars the service requires at startup.
			return nil, nil, fmt.Errorf("grant %q: grantable type %q has no deploy-side resolver — add one before wiring it into grant.For", g.Resource, typ)
		}

		target := pkiGrantTarget(name)
		material, ok := all.PKI[target]
		if !ok {
			return nil, nil, fmt.Errorf("grant %q: pki %q not found in the environment's store", g.Resource, target)
		}
		// The store is env-scoped and keyed by bare name, so nothing about the lookup
		// enforces the scope boundary — check it explicitly. Without this, a regional
		// service granting a regional PKI would resolve whatever single entry carries
		// that name (possibly a global CA), and two regions granting the same regional
		// PKI resource would silently share one root key, which a per-region PKI must
		// never do.
		if want := pkiGrantScope(name, region); material.Scope != want {
			return nil, nil, fmt.Errorf("grant %q: pki %q serves scope %q, but this service is in scope %q — a grant may not cross scopes (name a global PKI as %q)", g.Resource, target, material.Scope, want, types.GlobalPrefix+target)
		}
		fields, err := grant.PKIResource{Cert: material.Cert, Key: material.Key}.
			Grant(ctx, svc.Name, grant.Permission(g.Permission), env, region)
		if err != nil {
			return nil, nil, fmt.Errorf("grant %q: %w", g.Resource, err)
		}

		for _, envName := range sortedKeys(g.Outputs) {
			field, err := fileField(g.Outputs[envName])
			if err != nil {
				return nil, nil, fmt.Errorf("grant %q output %q: %w", g.Resource, envName, err)
			}
			fm, ok := fields.Files[field]
			if !ok {
				return nil, nil, fmt.Errorf("grant %q output %q: field %q is not published for permission %q", g.Resource, envName, field, g.Permission)
			}
			key := grant.FileKey(target, field)
			descriptorFiles[envName] = key
			blobFiles[key] = fm.PEM
		}
	}
	if len(descriptorFiles) == 0 {
		return nil, nil, nil
	}
	return descriptorFiles, blobFiles, nil
}

// fileField resolves a file-field outputs: template to the single {FIELD} it
// names. A file field is a PATH, not a composable string, so its template must be
// exactly one placeholder with no literal text — the
// grant-file-field-output-must-stand-alone rule, which validate.checkGrants
// enforces at authoring time and this re-checks at the materialization seam.
func fileField(tmpl string) (string, error) {
	t, err := grant.ParseTemplate(tmpl)
	if err != nil {
		return "", err
	}
	fields := t.Fields()
	if t.HasLiteral() || len(fields) != 1 {
		return "", fmt.Errorf("a file-field template must be exactly one {FIELD} placeholder with no surrounding text, got %q", tmpl)
	}
	return fields[0], nil
}
