// Package infisical implements the Infisical secrets provider for inforge. The
// InfisicalSecretsAdapter creates one InfisicalWorkspace per (container, region)
// pair — mirroring the NeonDatabaseAdapter container-dedup pattern — and, per
// service, writes the service's infra secrets under its scoped path and mints a
// per-service machine identity (ProvisionService) so inforge-bootstrap fetches
// those secrets at runtime via the Infisical Universal Auth flow.
package infisical

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/validate"
)

// Pulumi type tokens for the custom resources served by pulumi-resource-infisical.
const (
	infisicalWorkspaceType    = "infisical:resources:InfisicalWorkspace"
	infisicalSecretsBatchType = "infisical:resources:InfisicalSecretsBatch"
	infisicalIdentityType     = "infisical:resources:InfisicalIdentity"
)

// infisicalWorkspaceResource is the output state returned by the Pulumi engine
// after an InfisicalWorkspace is created.
type infisicalWorkspaceResource struct {
	pulumi.CustomResourceState
	WorkspaceId pulumi.StringOutput `pulumi:"workspaceId"`
}

func newInfisicalWorkspaceResource(
	ctx *pulumi.Context, name, workspaceName, clientId, clientSecret, siteUrl string,
	opts ...pulumi.ResourceOption,
) (*infisicalWorkspaceResource, error) {
	res := &infisicalWorkspaceResource{}
	args := pulumi.Map{
		"name":         pulumi.String(workspaceName),
		"clientId":     pulumi.String(clientId),
		"clientSecret": pulumi.String(clientSecret),
		"siteUrl":      pulumi.String(siteUrl),
	}
	if err := ctx.RegisterResource(infisicalWorkspaceType, name, args, res, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// infisicalSecretsBatchResource is the output state returned by the Pulumi
// engine after an InfisicalSecretsBatch is created. It carries no additional
// outputs beyond its inputs.
type infisicalSecretsBatchResource struct {
	pulumi.CustomResourceState
}

func newInfisicalSecretsBatchResource(
	ctx *pulumi.Context, name string,
	workspaceId pulumi.StringOutput, envSlug, clientId, clientSecret, siteUrl, secretPath string,
	secretsJson pulumi.StringOutput,
	opts ...pulumi.ResourceOption,
) (*infisicalSecretsBatchResource, error) {
	res := &infisicalSecretsBatchResource{}
	args := pulumi.Map{
		"workspaceId":  workspaceId,
		"envSlug":      pulumi.String(envSlug),
		"clientId":     pulumi.String(clientId),
		"clientSecret": pulumi.String(clientSecret),
		"siteUrl":      pulumi.String(siteUrl),
		"secretPath":   pulumi.String(secretPath),
		"secretsJson":  secretsJson,
	}
	if err := ctx.RegisterResource(infisicalSecretsBatchType, name, args, res, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// infisicalIdentityResource is the output state returned by the Pulumi engine
// after an InfisicalIdentity is created: the minted universal-auth credentials
// the on-host bootstrapper logs in with.
type infisicalIdentityResource struct {
	pulumi.CustomResourceState
	AuthClientId     pulumi.StringOutput `pulumi:"authClientId"`
	AuthClientSecret pulumi.StringOutput `pulumi:"authClientSecret"`
}

func newInfisicalIdentityResource(
	ctx *pulumi.Context, name string,
	workspaceId pulumi.StringOutput, envSlug, secretPath, clientId, clientSecret, siteUrl, organizationId string,
	opts ...pulumi.ResourceOption,
) (*infisicalIdentityResource, error) {
	res := &infisicalIdentityResource{}
	args := pulumi.Map{
		"name":           pulumi.String(name),
		"workspaceId":    workspaceId,
		"envSlug":        pulumi.String(envSlug),
		"secretPath":     pulumi.String(secretPath),
		"clientId":       pulumi.String(clientId),
		"clientSecret":   pulumi.String(clientSecret),
		"siteUrl":        pulumi.String(siteUrl),
		"organizationId": pulumi.String(organizationId),
	}
	if err := ctx.RegisterResource(infisicalIdentityType, name, args, res, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// InfisicalSecretsAdapter implements types.ServiceSecretsProvisioner using the
// custom pulumi-resource-infisical provider binary. One InfisicalWorkspace is
// created per (container, region)
// key; concurrent callers for the same key wait and reuse the same resource via
// a mutex-protected map.
type InfisicalSecretsAdapter struct {
	clientId       string
	clientSecret   string
	siteUrl        string
	organizationId string
	slug           string
	mu             sync.Mutex
	workspaces     map[string]pulumi.StringOutput
}

// New returns an InfisicalSecretsAdapter configured with the given credentials
// and region slug. organizationId is optional: when empty, identity
// provisioning derives the org from the access token's JWT (see resolveOrgID in
// the provider plugin); set it for Infisical deployments whose token carries no
// organizationId claim.
func New(clientId, clientSecret, siteUrl, organizationId, slug string) *InfisicalSecretsAdapter {
	if siteUrl == "" {
		siteUrl = "https://app.infisical.com"
	}
	return &InfisicalSecretsAdapter{
		clientId:       clientId,
		clientSecret:   clientSecret,
		siteUrl:        siteUrl,
		organizationId: organizationId,
		slug:           slug,
		workspaces:     map[string]pulumi.StringOutput{},
	}
}

// ProvisionService writes a service's infra secrets under "/<svc>/infra" and
// mints a per-service machine identity scoped read-only to "/<svc>", returning
// the bundle the program needs to write the descriptor + host-key-encrypted
// credential. It returns a nil bundle (no error) when the service has no infra
// secrets to deliver. Multiple services in the same container each get their own
// path and identity (the container's secrets are broadcast to every consuming
// service), so a leaked credential exposes only that one service's path.
func (a *InfisicalSecretsAdapter) ProvisionService(
	ctx *pulumi.Context, svc types.ServiceSpec, env, region string, all types.AllOutputs, grantSecrets map[string]pulumi.StringOutput,
) (*types.ServiceSecretsBundle, error) {
	// A service is provisioned when it has infra secrets, grant secrets, OR is a
	// mesh member: a `pki:`-only service still needs a workspace + identity so the
	// host can fetch the mesh leaf `inforge pki renew` writes under /<svc>/mtls (the
	// identity's read scope on /<svc> covers it).
	if len(svc.Environment) == 0 && len(grantSecrets) == 0 && svc.Pki == "" {
		return nil, nil
	}

	workspaceId, err := a.ensureWorkspace(ctx, svc.Container, env)
	if err != nil {
		return nil, fmt.Errorf("ensure infisical workspace for service %q: %w", svc.Name, err)
	}

	secretPath := servicePath(svc.Name)

	// Infra secrets are written only when the service declares them; a mesh-only
	// service skips the batch write but still gets an identity below.
	envMap := map[string]string{}
	if len(svc.Environment) > 0 || len(grantSecrets) > 0 {
		infraPath := secretPath + "/infra"
		// One infra batch carries both the environment.yaml-derived secrets and the
		// grant value secrets (ADR-0025). For each entry, infisicalKeys[i] is the key
		// stored in Infisical and ifaces[i] is its resolved StringOutput value.
		// Pre-compute the Infisical key for each env var: for vault: sources it is the
		// VaultKey (decoupled from the env var name); otherwise the env var name
		// doubles as the Infisical key. Grant value secrets are already resolved and
		// use the env var name as the key.
		envKeys := sortedStringKeys(svc.Environment)
		grantKeys := sortedStringKeys(grantSecrets)
		infisicalKeys := make([]string, 0, len(envKeys)+len(grantKeys))
		ifaces := make([]any, 0, len(envKeys)+len(grantKeys))
		for _, key := range envKeys {
			src, _ := validate.ParseSource(svc.Environment[key])
			ik := key
			if src.Kind == validate.SourceVault {
				ik = src.VaultKey
			}
			resolved, err := resolveRef(svc.Environment[key], svc.Container, region, all)
			if err != nil {
				return nil, fmt.Errorf("resolve ref for service %q secret %q: %w", svc.Name, key, err)
			}
			infisicalKeys = append(infisicalKeys, ik)
			ifaces = append(ifaces, resolved)
			envMap[key] = "infra/" + ik
		}
		for _, key := range grantKeys {
			infisicalKeys = append(infisicalKeys, key)
			ifaces = append(ifaces, grantSecrets[key])
			envMap[key] = "infra/" + key
		}
		secretsJson := pulumi.All(ifaces...).ApplyT(func(args []any) (string, error) {
			m := make(map[string]string, len(infisicalKeys))
			for i, k := range infisicalKeys {
				m[k] = args[i].(string)
			}
			b, err := json.Marshal(m)
			if err != nil {
				return "", fmt.Errorf("marshal secrets: %w", err)
			}
			return string(b), nil
		}).(pulumi.StringOutput)

		batchName := naming.Resource(env, a.slug, "secrets", svc.Name)
		if _, err := newInfisicalSecretsBatchResource(
			ctx, batchName,
			workspaceId, envToSlug(env), a.clientId, a.clientSecret, a.siteUrl, infraPath,
			secretsJson,
		); err != nil {
			return nil, fmt.Errorf("write infra secrets for service %q: %w", svc.Name, err)
		}
	}

	identityName := naming.Resource(env, a.slug, "identity", svc.Name)
	idRes, err := newInfisicalIdentityResource(
		ctx, identityName,
		workspaceId, envToSlug(env), secretPath, a.clientId, a.clientSecret, a.siteUrl, a.organizationId,
	)
	if err != nil {
		return nil, fmt.Errorf("provision identity for service %q: %w", svc.Name, err)
	}

	return &types.ServiceSecretsBundle{
		Project:      workspaceId,
		ClientID:     idRes.AuthClientId,
		ClientSecret: idRes.AuthClientSecret,
		ProviderKind: "infisical",
		URL:          a.siteUrl,
		Environment:  envToSlug(env),
		SecretPath:   secretPath,
		Env:          envMap,
	}, nil
}

// ensureWorkspace returns the workspaceId output for the (container, env)
// pair, creating the InfisicalWorkspace resource on first call for that key.
func (a *InfisicalSecretsAdapter) ensureWorkspace(
	ctx *pulumi.Context, container, env string,
) (pulumi.StringOutput, error) {
	key := fmt.Sprintf("%s-%s", container, env)

	a.mu.Lock()
	defer a.mu.Unlock()

	if ws, ok := a.workspaces[key]; ok {
		return ws, nil
	}

	wsName := a.workspaceName(container, env)
	wsRes, err := newInfisicalWorkspaceResource(ctx, wsName, wsName, a.clientId, a.clientSecret, a.siteUrl)
	if err != nil {
		return pulumi.StringOutput{}, fmt.Errorf("create infisical workspace %q: %w", wsName, err)
	}

	a.workspaces[key] = wsRes.WorkspaceId
	return wsRes.WorkspaceId, nil
}

// envToSlug maps the abstract environment name to the Infisical environment slug.
// Infisical uses "prod" not "prd".
func envToSlug(env string) string {
	if env == "prd" {
		return "prod"
	}
	return env
}

// workspaceName is the deterministic Infisical project name for a (container,
// env) pair. The Pulumi deploy path (ensureWorkspace) and the imperative renew
// path (CertWriter) MUST address the same workspace, so both derive it here —
// the addressing is defined once, not duplicated.
func (a *InfisicalSecretsAdapter) workspaceName(container, env string) string {
	return naming.Resource(env, a.slug, "container", container)
}

// servicePath is the per-service secret root ("/<service>") both the deploy path
// (infra secrets under /<svc>/infra) and the renew path (mesh material under
// /<svc>/mtls) write beneath.
func servicePath(service string) string { return "/" + service }

// resolveRef resolves a secrets source string to a Pulumi StringOutput.
//
// Supported forms:
//   - ref:database/<name>.<output>        — REJECTED: a database exposes no output (use a grant)
//   - ref:compute/<name>.<output>         — looks up compute output (publicIp)
//   - ref:compute/global/<name>.<output>  — looks up a GLOBAL compute output
//   - ${NAME}                             — reads environment variable NAME
//   - static:<value> / value:<value>      — returns the literal value verbatim
//   - encrypted                           — serves the pre-decrypted store value
//
// A `global/` prefix on the referenced name (RefName == "global/<name>") is the
// one allowed cross-region reference: it resolves against the region-less global
// slot (all.Database["global"] / all.Compute["global"]) regardless of the
// service's own region. The lookup region and the bare name are derived once here.
//
// container addresses a vault: source's value in all.Encrypted — the program
// decrypts the env's committed store once, provider-neutrally, and this adapter
// only ever sees plaintext (ADR-0017).
func resolveRef(source, container, region string, all types.AllOutputs) (pulumi.StringOutput, error) {
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
		val, ok := all.Encrypted[container][src.VaultKey]
		if !ok {
			return pulumi.StringOutput{}, fmt.Errorf(
				"resolveRef %q: no decrypted value for vault key %q in container %q — is it in resources/<env>/secrets.enc.yaml and %s set?",
				source, src.VaultKey, container, secretstore.IdentityEnvVar)
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


func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
