// Package neon implements the Neon database provider for inforge. The
// NeonDatabaseAdapter creates one NeonProject per (container, Neon region) pair
// — mirroring the HetznerNetwork container-dedup pattern — and one NeonDatabase
// resource per spec. A deployment across multiple abstract regions therefore
// produces independent Neon projects and connection URLs per region.
package neon

import (
	"fmt"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/types"
)

// Pulumi type tokens for the custom resources served by pulumi-resource-neon.
// The "resources" module segment is derived from the Go package name of the
// resources sub-package inside the provider binary.
const (
	neonProjectType  = "neon:resources:NeonProject"
	neonDatabaseType = "neon:resources:NeonDatabase"
	neonRoleType     = "neon:resources:NeonRole"
)

// neonProjectResource is the output state returned by the Pulumi engine after
// a NeonProject is created.
type neonProjectResource struct {
	pulumi.CustomResourceState
	ProjectId pulumi.StringOutput `pulumi:"projectId"`
}

func newNeonProjectResource(
	ctx *pulumi.Context, name string,
	projectName, regionId, apiKey string,
	opts ...pulumi.ResourceOption,
) (*neonProjectResource, error) {
	res := &neonProjectResource{}
	args := pulumi.Map{
		"name":     pulumi.String(projectName),
		"regionId": pulumi.String(regionId),
		"apiKey":   pulumi.String(apiKey),
	}
	if err := ctx.RegisterResource(neonProjectType, name, args, res, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// neonDatabaseResource is the output state returned by the Pulumi engine after
// a NeonDatabase is created.
type neonDatabaseResource struct {
	pulumi.CustomResourceState
	BranchId      pulumi.StringOutput `pulumi:"branchId"`
	ConnectionUrl pulumi.StringOutput `pulumi:"connectionUrl"`
}

func newNeonDatabaseResource(
	ctx *pulumi.Context, name string,
	projectId pulumi.StringInput, branch, database, owner, apiKey string,
	opts ...pulumi.ResourceOption,
) (*neonDatabaseResource, error) {
	res := &neonDatabaseResource{}
	args := pulumi.Map{
		"projectId": projectId,
		"branch":    pulumi.String(branch),
		"database":  pulumi.String(database),
		"owner":     pulumi.String(owner),
		"apiKey":    pulumi.String(apiKey),
	}
	if err := ctx.RegisterResource(neonDatabaseType, name, args, res, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// neonRoleResource is the output state returned by the Pulumi engine after a
// NeonRole is created: the scoped per-service role's connection value fields.
type neonRoleResource struct {
	pulumi.CustomResourceState
	User     pulumi.StringOutput `pulumi:"user"`
	Password pulumi.StringOutput `pulumi:"password"`
	Host     pulumi.StringOutput `pulumi:"host"`
	Port     pulumi.StringOutput `pulumi:"port"`
	DBName   pulumi.StringOutput `pulumi:"dbName"`
	URL      pulumi.StringOutput `pulumi:"url"`
}

func newNeonRoleResource(
	ctx *pulumi.Context, name string,
	projectId, branchId pulumi.StringInput, database, roleName, permission string,
	ownerConnectionURI pulumi.StringInput, apiKey string,
	opts ...pulumi.ResourceOption,
) (*neonRoleResource, error) {
	res := &neonRoleResource{}
	args := pulumi.Map{
		"projectId":          projectId,
		"branchId":           branchId,
		"database":           pulumi.String(database),
		"roleName":           pulumi.String(roleName),
		"permission":         pulumi.String(permission),
		"ownerConnectionUri": ownerConnectionURI,
		"apiKey":             pulumi.String(apiKey),
	}
	if err := ctx.RegisterResource(neonRoleType, name, args, res, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// neonRoleProvisioner is the types.DBRoleProvisioner bound to one NeonDatabase. It
// captures only that database's coordinates (project, branch, name, owner
// connection), so it mints a role identically whether the consuming service is
// regional or reaches it through the global slot — the consumer-scoped roleName is
// supplied by the caller (ADR-0025).
type neonRoleProvisioner struct {
	projectId pulumi.StringOutput
	branchId  pulumi.StringOutput
	database  string
	ownerURI  pulumi.StringOutput
	apiKey    string
}

func (p *neonRoleProvisioner) ProvisionRole(
	ctx *pulumi.Context, roleName, permission string,
) (types.DBRoleFields, error) {
	res, err := newNeonRoleResource(ctx, roleName, p.projectId, p.branchId, p.database, roleName, permission, p.ownerURI, p.apiKey)
	if err != nil {
		return types.DBRoleFields{}, fmt.Errorf("create neon role %q: %w", roleName, err)
	}
	return types.DBRoleFields{
		User:     res.User,
		Password: res.Password,
		Host:     res.Host,
		Port:     res.Port,
		DBName:   res.DBName,
		URL:      res.URL,
	}, nil
}

// NeonDatabaseAdapter implements types.DatabaseProvider using the custom
// pulumi-resource-neon provider binary. One NeonProject is created per
// (container, Neon region) key; concurrent calls with the same key are
// deduplicated via a mutex-protected map, mirroring HetznerNetwork.
type NeonDatabaseAdapter struct {
	apiKey     string
	project    string
	slug       string
	region     string // explicit Neon region from provider config; overrides the abstract-region map when set
	mu         sync.Mutex
	containers map[string]*neonProjectResource // key → project resource
}

// New returns a NeonDatabaseAdapter configured with the given Neon API key,
// inforge project name, region slug, and an optional explicit Neon region. The
// explicit region (providers.neon.region in regions.yaml) is the physical Neon
// region the project lives in: it is required for the region-less global slice
// (which has no abstract region to map) and overrides the abstract-region map for
// any region that sets it. When empty, the abstract region is resolved through
// the built-in map (the regional default), so regional behavior is unchanged.
func New(apiKey, project, slug, region string) *NeonDatabaseAdapter {
	return &NeonDatabaseAdapter{
		apiKey:     apiKey,
		project:    project,
		slug:       slug,
		region:     region,
		containers: map[string]*neonProjectResource{},
	}
}

// Create provisions a NeonProject (deduped per container+region) and a
// NeonDatabase within it, then returns a per-database role provisioner (no
// credential-bearing output — ADR-0025).
func (n *NeonDatabaseAdapter) Create(
	ctx *pulumi.Context, spec types.DatabaseSpec, env, abstractRegion string,
) (types.DatabaseOutputs, error) {
	neonRegion, err := n.resolveRegion(abstractRegion)
	if err != nil {
		return types.DatabaseOutputs{}, err
	}

	proj, err := n.ensureContainer(ctx, spec.Container, env, neonRegion)
	if err != nil {
		return types.DatabaseOutputs{}, fmt.Errorf("ensure neon project for container %q in %s: %w", spec.Container, neonRegion, err)
	}

	branch := spec.Branch
	if branch == "" {
		branch = "main"
	}

	dbName := naming.Resource(env, n.slug, "db", spec.Name)
	dbRes, err := newNeonDatabaseResource(ctx, dbName, proj.ProjectId, branch, spec.Database, spec.Owner, n.apiKey)
	if err != nil {
		return types.DatabaseOutputs{}, fmt.Errorf("create neon database %q: %w", dbName, err)
	}

	// A database exposes no credential-bearing ref output (ADR-0025). Instead it
	// hands back a provisioner bound to this database; a service grant uses it to
	// mint a scoped per-service role. The owner connection (with the owner password)
	// stays inside the provisioner and is delivered only to the NeonRole resource —
	// never as a public output.
	return types.DatabaseOutputs{
		RoleProvisioner: &neonRoleProvisioner{
			projectId: proj.ProjectId,
			branchId:  dbRes.BranchId,
			database:  spec.Database,
			ownerURI:  dbRes.ConnectionUrl,
			apiKey:    n.apiKey,
		},
	}, nil
}

// ensureContainer returns the NeonProject resource for the (container, neonRegion)
// pair, creating it on first call for that key. The lock wraps both the lookup and
// the RegisterResource call so that concurrent callers for the same key wait and
// reuse the same resource, matching the HetznerNetwork pattern.
func (n *NeonDatabaseAdapter) ensureContainer(
	ctx *pulumi.Context, container, env, neonRegion string,
) (*neonProjectResource, error) {
	key := fmt.Sprintf("%s-%s", container, neonRegion)

	n.mu.Lock()
	defer n.mu.Unlock()

	if proj, ok := n.containers[key]; ok {
		return proj, nil
	}

	projectName := naming.Resource(env, n.slug, "container", container)
	res, err := newNeonProjectResource(ctx, projectName, projectName, neonRegion, n.apiKey)
	if err != nil {
		return nil, fmt.Errorf("create neon project %q: %w", projectName, err)
	}

	n.containers[key] = res
	return res, nil
}
