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
	projectId pulumi.StringInput, branch, database, role, apiKey string,
	opts ...pulumi.ResourceOption,
) (*neonDatabaseResource, error) {
	res := &neonDatabaseResource{}
	args := pulumi.Map{
		"projectId": projectId,
		"branch":    pulumi.String(branch),
		"database":  pulumi.String(database),
		"role":      pulumi.String(role),
		"apiKey":    pulumi.String(apiKey),
	}
	if err := ctx.RegisterResource(neonDatabaseType, name, args, res, opts...); err != nil {
		return nil, err
	}
	return res, nil
}

// NeonDatabaseAdapter implements types.DatabaseProvider using the custom
// pulumi-resource-neon provider binary. One NeonProject is created per
// (container, Neon region) key; concurrent calls with the same key are
// deduplicated via a mutex-protected map, mirroring HetznerNetwork.
type NeonDatabaseAdapter struct {
	apiKey     string
	project    string
	slug       string
	mu         sync.Mutex
	containers map[string]*neonProjectResource // key → project resource
}

// New returns a NeonDatabaseAdapter configured with the given Neon API key,
// inforge project name, and region slug.
func New(apiKey, project, slug string) *NeonDatabaseAdapter {
	return &NeonDatabaseAdapter{
		apiKey:     apiKey,
		project:    project,
		slug:       slug,
		containers: map[string]*neonProjectResource{},
	}
}

// Create provisions a NeonProject (deduped per container+region) and a
// NeonDatabase within it, then returns the connection URL as a secret output.
func (n *NeonDatabaseAdapter) Create(
	ctx *pulumi.Context, spec types.DatabaseSpec, env, abstractRegion string,
) (types.DatabaseOutputs, error) {
	neonRegion, err := ResolveRegion(abstractRegion)
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
	dbRes, err := newNeonDatabaseResource(ctx, dbName, proj.ProjectId, branch, spec.Database, spec.Role, n.apiKey)
	if err != nil {
		return types.DatabaseOutputs{}, fmt.Errorf("create neon database %q: %w", dbName, err)
	}

	return types.DatabaseOutputs{ConnectionURL: dbRes.ConnectionUrl}, nil
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
