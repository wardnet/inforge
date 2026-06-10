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
	"strings"
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
	ctx *pulumi.Context, svc types.ServiceSpec, res types.Resources, env, region string, all types.AllOutputs,
) (*types.ServiceSecretsBundle, error) {
	entries, err := infraSecretEntries(svc, res)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	workspaceId, err := a.ensureWorkspace(ctx, svc.Container, env)
	if err != nil {
		return nil, fmt.Errorf("ensure infisical workspace for service %q: %w", svc.Name, err)
	}

	secretPath := "/" + svc.Name
	infraPath := secretPath + "/infra"

	keys := sortedStringKeys(entries)
	ifaces := make([]interface{}, len(keys))
	for i, key := range keys {
		resolved, err := resolveRef(key, entries[key].Source, svc.Container, region, all)
		if err != nil {
			return nil, fmt.Errorf("resolve ref for service %q secret %q: %w", svc.Name, key, err)
		}
		ifaces[i] = resolved
	}
	secretsJson := pulumi.All(ifaces...).ApplyT(func(args []interface{}) (string, error) {
		m := make(map[string]string, len(keys))
		for i, key := range keys {
			m[key] = args[i].(string)
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

	identityName := naming.Resource(env, a.slug, "identity", svc.Name)
	idRes, err := newInfisicalIdentityResource(
		ctx, identityName,
		workspaceId, envToSlug(env), secretPath, a.clientId, a.clientSecret, a.siteUrl, a.organizationId,
	)
	if err != nil {
		return nil, fmt.Errorf("provision identity for service %q: %w", svc.Name, err)
	}

	envMap := make(map[string]string, len(keys))
	for _, key := range keys {
		envMap[key] = "infra/" + key
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

// infraSecretEntries returns the secret entries a service consumes, merged from
// every infisical SecretsSpec in the service's container. The map key is both the
// vault key written under infra/ and the env var name the bootstrapper sets, so
// all services in a container share the same secret set (broadcast semantics —
// the schema links services to secrets only by container). Two specs in the same
// container declaring the same key is ambiguous (which value wins?), so it is an
// error rather than a silent last-write-wins.
func infraSecretEntries(svc types.ServiceSpec, res types.Resources) (map[string]types.SecretsEntry, error) {
	out := map[string]types.SecretsEntry{}
	for _, s := range res.Secrets {
		if s.Container != svc.Container || s.Provider != "infisical" {
			continue
		}
		for k, v := range s.Secrets {
			if _, dup := out[k]; dup {
				return nil, fmt.Errorf("service %q: secret key %q is declared by more than one secrets spec in container %q", svc.Name, k, svc.Container)
			}
			out[k] = v
		}
	}
	return out, nil
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

	wsName := naming.Resource(env, a.slug, "container", container)
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

// resolveRef resolves a secrets source string to a Pulumi StringOutput.
//
// Supported forms:
//   - ref:database/<name>.<output>        — looks up database output (connectionUrl)
//   - ref:compute/<name>.<output>         — looks up compute output (publicIp)
//   - ref:database/global/<name>.<output> — looks up a GLOBAL database output
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
// key and container address an `encrypted` source's value in all.Encrypted —
// the program decrypts the env's committed store once, provider-neutrally, and
// this adapter only ever sees plaintext (ADR-0017).
func resolveRef(key, source, container, region string, all types.AllOutputs) (pulumi.StringOutput, error) {
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

	case validate.SourceStatic:
		return pulumi.String(src.StaticValue).ToStringOutput(), nil

	case validate.SourceEncrypted:
		val, ok := all.Encrypted[container][key]
		if !ok {
			return pulumi.StringOutput{}, fmt.Errorf(
				"resolveRef %q: no decrypted value for container %q key %q — is the entry in resources/<env>/secrets.enc.yaml and %s set?",
				source, container, key, secretstore.IdentityEnvVar)
		}
		// Marked secret so the plaintext is encrypted in Pulumi state and masked
		// in console/diff output (the other kinds reference values that already
		// live elsewhere; this one exists only as ciphertext in git).
		return pulumi.ToSecret(pulumi.String(val)).(pulumi.StringOutput), nil

	case validate.SourceRef:
		// A global/ prefix redirects the lookup to the global slot, independent of
		// the consuming service's region.
		name := src.RefName
		if rest, ok := strings.CutPrefix(src.RefName, "global/"); ok {
			region = "global"
			name = rest
		}
		switch src.RefType {
		case "database":
			regionMap, ok := all.Database[region]
			if !ok {
				return pulumi.StringOutput{}, fmt.Errorf(
					"resolveRef %q: no database outputs for region %q (available: %v)",
					source, region, sortedKeys(all.Database),
				)
			}
			db, ok := regionMap[name]
			if !ok {
				return pulumi.StringOutput{}, fmt.Errorf(
					"resolveRef %q: no database %q in region %q (available: %v)",
					source, name, region, sortedStringKeys(regionMap),
				)
			}
			if src.RefOutput != "connectionUrl" {
				return pulumi.StringOutput{}, fmt.Errorf(
					"resolveRef %q: unknown database output field %q (available: connectionUrl)",
					source, src.RefOutput,
				)
			}
			return db.ConnectionURL, nil

		case "compute":
			regionMap, ok := all.Compute[region]
			if !ok {
				return pulumi.StringOutput{}, fmt.Errorf(
					"resolveRef %q: no compute outputs for region %q (available: %v)",
					source, region, sortedKeys(all.Compute),
				)
			}
			comp, ok := regionMap[name]
			if !ok {
				return pulumi.StringOutput{}, fmt.Errorf(
					"resolveRef %q: no compute instance %q in region %q (available: %v)",
					source, name, region, sortedStringKeys(regionMap),
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

func sortedKeys[V any](m map[string]map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
