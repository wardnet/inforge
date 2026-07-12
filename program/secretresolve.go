package program

import (
	"context"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/yamldoc"
)

// A service's environment.yaml speaks the same DSL as every other file —
// env:/vault:/ref:, whole-value, anything else a literal — but its values are not
// strings: they are pulumi.StringOutputs. A ref:compute/edge.publicIp is not known
// until the host exists, and a vault: value is marked secret so its plaintext is
// encrypted in Pulumi state and masked in diffs. Same grammar, same dispatch,
// different value type — which is why yamldoc.Resolver is generic.
//
// The chain IS the capability. It is built per service, from exactly what that
// service may see: its OWN decrypted secrets — keyed by the declaring service, never
// by its container (ADR-0040) — and the compute outputs of its own region plus the
// global slot. Nothing else is reachable, because no other resolver is in the chain.
// Resource manifests are decoded with an env-only chain, so a vault: reference in a
// compute manifest is not a secret leak — it is an unclaimed literal.
func serviceChain(service, region string, all types.AllOutputs) yamldoc.Chain[pulumi.StringOutput] {
	return yamldoc.Chain[pulumi.StringOutput]{
		envSource{},
		vaultSource{service: service, encrypted: all.Encrypted},
		refSource{region: region, compute: all.Compute},
	}
}

// resolveRef resolves one environment.yaml value to the Pulumi value the service's
// secrets.age is built from. A value no resolver claims is a LITERAL — a verbatim
// inline value, committed in plaintext, for non-secret config.
func resolveRef(source, service, region string, all types.AllOutputs) (pulumi.StringOutput, error) {
	v, claimed, err := serviceChain(service, region, all).Resolve(context.Background(), source)
	if err != nil {
		return pulumi.StringOutput{}, fmt.Errorf("resolve %q: %w", source, err)
	}
	if !claimed {
		return pulumi.String(source).ToStringOutput(), nil
	}
	return v, nil
}

// envSource reads the value from the deploy process environment. The consumer
// injects it however they like (e.g. a GitHub Actions secret mapped to an env var).
// An unset or empty value is a misconfiguration and must fail loudly rather than
// materialise an empty secret.
type envSource struct{}

func (envSource) Name() string            { return "env" }
func (envSource) Matches(raw string) bool { return hasScheme(yamldoc.EnvScheme, raw) }

func (envSource) Resolve(_ context.Context, raw string) (pulumi.StringOutput, error) {
	name, _, err := yamldoc.ParseKey(yamldoc.EnvScheme, raw)
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	val := os.Getenv(name)
	if val == "" {
		return pulumi.StringOutput{}, fmt.Errorf(
			"environment variable %s is empty or unset — inject it into the deploy step's environment (e.g. from a CI secret)", name)
	}
	return pulumi.String(val).ToStringOutput(), nil
}

// vaultSource serves a value from the env's committed, age-encrypted secret store
// (ADR-0017), decrypted once by the program. This resolver only ever sees plaintext,
// and only the secrets of the service that declared them (ADR-0040) — a secret's
// blast radius is the unit that rotates it.
type vaultSource struct {
	service   string
	encrypted map[string]map[string]string
}

func (vaultSource) Name() string            { return "vault" }
func (vaultSource) Matches(raw string) bool { return hasScheme(yamldoc.VaultScheme, raw) }

func (v vaultSource) Resolve(_ context.Context, raw string) (pulumi.StringOutput, error) {
	key, _, err := yamldoc.ParseKey(yamldoc.VaultScheme, raw)
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	val, ok := v.encrypted[v.service][key]
	if !ok {
		return pulumi.StringOutput{}, fmt.Errorf(
			"no decrypted value for vault key %q of service %q — is it in resources/<env>/secrets.enc.yaml and %s set?",
			key, v.service, secretstore.IdentityEnvVar)
	}
	// Marked secret so the plaintext is encrypted in Pulumi state and masked in
	// console/diff output.
	return pulumi.ToSecret(pulumi.String(val)).(pulumi.StringOutput), nil
}

// refSource resolves another resource's output. A `global/` prefix on the name is
// the one allowed cross-region reference: it resolves against the region-less global
// slot regardless of the consuming service's own region.
type refSource struct {
	region  string
	compute map[string]map[string]types.ComputeOutputs
}

func (refSource) Name() string            { return "ref" }
func (refSource) Matches(raw string) bool { return hasScheme(yamldoc.RefScheme, raw) }

func (r refSource) Resolve(_ context.Context, raw string) (pulumi.StringOutput, error) {
	ref, _, err := yamldoc.ParseRef(raw)
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	switch ref.Type {
	case "database":
		// A database exposes no referenceable outputs (ADR-0025): the credential-bearing
		// connectionUrl was removed so the admin credential is never handed to consumers.
		// DB credentials flow only through a grant. inforge validate rejects this
		// earlier; this guards the deploy path.
		return pulumi.StringOutput{}, fmt.Errorf(
			"a database exposes no referenceable outputs; use a grants: entry for DB credentials (ADR-0025)")

	case "compute":
		comp, refRegion, name, found := types.ResolveScoped(r.compute, r.region, ref.Name)
		if !found {
			return pulumi.StringOutput{}, fmt.Errorf("no compute instance %q in region %q", name, refRegion)
		}
		if ref.Output != "publicIp" {
			return pulumi.StringOutput{}, fmt.Errorf("unknown compute output field %q (available: publicIp)", ref.Output)
		}
		return comp.PublicIP, nil

	default:
		return pulumi.StringOutput{}, fmt.Errorf("unknown ref type %q (supported: database, compute)", ref.Type)
	}
}

// hasScheme reports whether raw carries a scheme. A MALFORMED reference under a
// recognised scheme (`vault:` with no key) is still claimed — so it fails loudly
// rather than being mistaken for a literal and committed verbatim as a value.
func hasScheme(scheme, raw string) bool {
	return len(raw) >= len(scheme) && raw[:len(scheme)] == scheme
}
