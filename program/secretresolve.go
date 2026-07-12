package program

import (
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/validate"
)

// resolveRef resolves a service's environment.yaml secrets source string to a
// Pulumi StringOutput. This is the provider-agnostic value resolver ADR-0035
// relocates out of the (retired) Infisical adapter: with no runtime secrets
// backend, a source string is resolved directly, at deploy time, into the
// service's persistent secrets.age (internal/hostsecrets) — there is no
// provider workspace/environment/secret_path addressing left to thread through.
//
// Supported forms:
//   - ref:database/<name>.<output>        — REJECTED: a database exposes no output (use a grant)
//   - ref:compute/<name>.<output>         — looks up compute output (publicIp)
//   - ref:compute/global/<name>.<output>  — looks up a GLOBAL compute output
//   - ${NAME}                             — reads environment variable NAME
//   - static:<value> / value:<value>      — returns the literal value verbatim
//   - vault:<key>                         — serves the pre-decrypted store value
//
// A `global/` prefix on the referenced name (RefName == "global/<name>") is the
// one allowed cross-region reference: it resolves against the region-less global
// slot (all.Compute["global"]) regardless of the service's own region. The lookup
// region and the bare name are derived once here.
//
// service addresses a vault: source's value in all.Encrypted — the program
// decrypts the env's committed store once, provider-neutrally, and this resolver
// only ever sees plaintext (ADR-0017). Secrets are keyed by the SERVICE that
// declares the reference, never by its container (ADR-0040).
func resolveRef(source, service, region string, all types.AllOutputs) (pulumi.StringOutput, error) {
	src, err := validate.ParseSource(source)
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	switch src.Kind {
	case validate.SourceEnv:
		// The value is read from the deploy process environment — the same
		// ${ENV_VAR} convention the loader applies to variables.yaml/regions.yaml.
		// The consumer injects it however they like (e.g. a GitHub Actions secret
		// mapped to an env var in their workflow). An unset/empty value is a
		// misconfiguration and must fail loudly rather than materialise an empty
		// secret.
		val := os.Getenv(src.EnvName)
		if val == "" {
			return pulumi.StringOutput{}, fmt.Errorf(
				"resolveRef %q: environment variable %s is empty or unset — inject it into the deploy step's environment (e.g. from a CI secret)",
				source, src.EnvName)
		}
		return pulumi.String(val).ToStringOutput(), nil

	case validate.SourceLiteral:
		return pulumi.String(src.LiteralValue).ToStringOutput(), nil

	case validate.SourceVault:
		val, ok := all.Encrypted[service][src.VaultKey]
		if !ok {
			return pulumi.StringOutput{}, fmt.Errorf(
				"resolveRef %q: no decrypted value for vault key %q of service %q — is it in resources/<env>/secrets.enc.yaml and %s set?",
				source, src.VaultKey, service, secretstore.IdentityEnvVar)
		}
		// Marked secret so the plaintext is encrypted in Pulumi state and masked
		// in console/diff output.
		return pulumi.ToSecret(pulumi.String(val)).(pulumi.StringOutput), nil

	case validate.SourceRef:
		switch src.RefType {
		case "database":
			// A database exposes no referenceable outputs (ADR-0025): the
			// credential-bearing connectionUrl was removed so the admin credential is
			// never handed to consumers. DB credentials flow only through a grant.
			// inforge validate rejects this earlier; this guards the deploy path.
			return pulumi.StringOutput{}, fmt.Errorf(
				"resolveRef %q: a database exposes no referenceable outputs; use a grants: entry for DB credentials (ADR-0025)",
				source,
			)

		case "compute":
			// Shared global/ redirect (a global/ prefix resolves against the global
			// slot, independent of the consuming service's region).
			comp, refRegion, name, found := types.ResolveScoped(all.Compute, region, src.RefName)
			if !found {
				return pulumi.StringOutput{}, fmt.Errorf(
					"resolveRef %q: no compute instance %q in region %q",
					source, name, refRegion,
				)
			}
			if src.RefOutput != "publicIp" {
				return pulumi.StringOutput{}, fmt.Errorf(
					"resolveRef %q: unknown compute output field %q (available: publicIp)",
					source, src.RefOutput,
				)
			}
			return comp.PublicIP, nil

		default:
			return pulumi.StringOutput{}, fmt.Errorf(
				"resolveRef %q: unknown ref type %q (supported: database, compute)",
				source, src.RefType,
			)
		}

	default:
		return pulumi.StringOutput{}, fmt.Errorf("resolveRef %q: unhandled source kind", source)
	}
}
