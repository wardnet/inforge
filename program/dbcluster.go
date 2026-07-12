package program

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/postgres"
	"github.com/wardnet/inforge/internal/registry"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
)

// clusterPortsByHost assigns each database-cluster its deterministic TCP port on its
// host (ADR-0036): clusters co-located on one host are sorted by name and given
// postgres.ClusterPort(index). Within one deploy the firewall (which opens each
// cluster's port privately) and the realization (which starts each postmaster on it)
// call this and therefore agree — it is the single source of the port scheme. The
// assignment is by SORTED POSITION, so adding or removing a co-located cluster can
// shift the ports of clusters that sort after it, restarting their postmasters onto a
// new port on the next deploy (the data directory is keyed by cluster name, not port,
// so no data is lost). This positional scheme mirrors the mesh egress-port assignment.
// Keyed by canonical host specKey, then cluster name -> port. Clusters whose host FK
// does not resolve are skipped (validation reports them).
func clusterPortsByHost(res types.Resources, canonical map[string]string) map[string]map[string]int {
	byHost := map[string][]string{}
	for _, c := range res.DatabaseCluster {
		hk, ok := canonical[c.Host]
		if !ok {
			continue
		}
		byHost[hk] = append(byHost[hk], c.Name)
	}
	out := map[string]map[string]int{}
	for hk, names := range byHost {
		sort.Strings(names)
		out[hk] = map[string]int{}
		for i, n := range names {
			out[hk][n] = postgres.ClusterPort(i)
		}
	}
	return out
}

// clusterVolumeSizeGB is the raw sum of a cluster's logical database sizes. Postgres
// has no per-database quota, so the cluster's single volume is sized from the sum
// (ADR-0036); the storage provider floors it at its own minimum, so a zero sum still
// yields a valid volume.
func clusterVolumeSizeGB(res types.Resources, cluster string) int {
	sum := 0
	for _, d := range res.Database {
		if d.Cluster == cluster {
			sum += d.SizeGB
		}
	}
	return sum
}

// databasesOfCluster returns the logical databases belonging to a cluster, sorted by
// name for a deterministic realization order.
func databasesOfCluster(res types.Resources, cluster string) []types.DatabaseSpec {
	var out []types.DatabaseSpec
	for _, d := range res.Database {
		if d.Cluster == cluster {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// provisionDatabaseClusters realizes every self-hosted database-cluster in one scope
// (ADR-0036) and populates dbOut with one DatabaseOutputs per logical database, each
// carrying a DBRoleProvisioner bound to that database's cluster host. Per cluster it:
// resolves the host and its persistent data volume (reg.Storage), installs the
// version-pinned Postgres package over SSH, formats/mounts the volume, initdb's the
// PGDATA on it, writes the config + starts the postmaster, and ensures each logical
// database plus its NOLOGIN owner exist. It runs AFTER attachPrivateNetworks (it needs
// each host's private IP for listen_addresses and the returned connection Host) and
// BEFORE provisionServiceSecrets (which resolves grants against dbOut). The 5432-range
// ports are opened to the private CIDR only by firewallPlanByHost.
//
// It returns the per-host tail: the last remote command run on each cluster host,
// keyed by canonical host specKey. The backup pass (provisionDatabaseBackups)
// DependsOn it so its awscli apt install never races the Postgres apt install on the
// same host, and so a backup timer is installed only after its cluster is up.
func provisionDatabaseClusters(ctx *pulumi.Context, reg registry.ProviderRegistry, res types.Resources, computeOut map[string]types.ComputeOutputs, dbOut map[string]types.DatabaseOutputs, gates map[string]pulumi.Resource, defaults types.ProviderDefaults, deployPrivateKey, env, region, slug string) (map[string]pulumi.Resource, error) {
	if len(res.DatabaseCluster) == 0 {
		return nil, nil
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)
	deployUserByCompute := naming.DeployUsersByHost(res.Compute)
	netCIDR := networkCIDRByCompute(res, canonical)
	ports := clusterPortsByHost(res, canonical)
	computeByName := map[string]types.ComputeSpec{}
	for _, c := range res.Compute {
		computeByName[c.Name] = c
	}
	// hostLast chains clusters co-located on one host: each cluster's first command
	// waits on the previous cluster's last command on that host, so two clusters never
	// run `apt-get install` (or append /etc/fstab) concurrently and race the dpkg lock.
	// Clusters on different hosts stay parallel.
	hostLast := map[string]pulumi.Resource{}

	for _, cluster := range res.DatabaseCluster {
		effective := types.ResolveProvider(cluster.Provider, "database", cluster.Engine, defaults)
		if effective != types.SelfHostedProvider {
			// The managed DatabaseProvider seam is retained (types.DatabaseProvider) but
			// no managed provider is registered — ADR-0036 retired Neon. Only self-hosted
			// realizes here; a managed cluster is a configuration error until one re-adds.
			return nil, fmt.Errorf("database-cluster %q: provider %q is not available — ADR-0036 retired the managed database provider; use provider: %s", cluster.Name, effective, types.SelfHostedProvider)
		}
		hostKey, ok := canonical[cluster.Host]
		if !ok {
			return nil, fmt.Errorf("database-cluster %q: host %q does not resolve to a compute in this scope", cluster.Name, cluster.Host)
		}
		host, ok := computeOut[hostKey]
		if !ok {
			return nil, fmt.Errorf("database-cluster %q: host %q has no compute output", cluster.Name, cluster.Host)
		}
		deployUser := deployUserByCompute[hostKey]
		if !ctx.DryRun() {
			if deployUser == "" {
				return nil, fmt.Errorf("database-cluster %q: host %q has no deploy_user; inforge needs one to SSH and install Postgres", cluster.Name, cluster.Host)
			}
			if deployPrivateKey == "" {
				return nil, fmt.Errorf("database-cluster %q: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)", cluster.Name)
			}
		}
		port := ports[hostKey][cluster.Name]
		cidr := netCIDR[hostKey]

		gate, err := cloudInitGate(ctx, gates, hostKey, host, deployPrivateKey, env, slug)
		if err != nil {
			return nil, err
		}
		conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
		name := naming.Resource(env, slug, "db", cluster.Name)

		// Persistent data volume: size = sum of the cluster's logical database sizes
		// (the provider floors it at its own minimum). The whole PGDATA lives on it, so
		// reattaching it to a rebuilt host recovers every database. The storage provider
		// is the cluster HOST's compute provider (the volume attaches to that server).
		storage, err := reg.Storage(types.ResolveProvider(computeByName[cluster.Host].Provider, "compute", "", defaults))
		if err != nil {
			return nil, fmt.Errorf("database-cluster %q: %w", cluster.Name, err)
		}
		vol, err := storage.CreateVolume(ctx, types.StorageRequest{
			Name:        cluster.Name,
			Env:         env,
			Region:      region,
			Container:   cluster.Container,
			HostSpecKey: hostKey,
			SizeGB:      clusterVolumeSizeGB(res, cluster.Name),
		}, []pulumi.Resource{gate})
		if err != nil {
			return nil, fmt.Errorf("database-cluster %q: create volume: %w", cluster.Name, err)
		}

		// 1) Install the version-pinned Postgres server package. Serialize with any
		// prior cluster on this host (hostLast) so co-located apt installs don't race.
		installDeps := []pulumi.Resource{gate}
		if prev := hostLast[hostKey]; prev != nil {
			installDeps = append(installDeps, prev)
		}
		install := postgres.InstallScript(cluster.Version)
		installCmd, err := remote.NewCommand(ctx, name+"-install", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String(install),
			Update:     pulumi.String(install),
			Triggers:   pulumi.Array{pulumi.String(install)},
		}, pulumi.DependsOn(installDeps))
		if err != nil {
			return nil, fmt.Errorf("database-cluster %q: install postgres: %w", cluster.Name, err)
		}

		// 2) Format (only if unformatted) + mount the data volume; the device path is
		// known only at apply, and the mount must wait on the attachment.
		mountScript := vol.DevicePath.ApplyT(func(dev string) string {
			return postgres.MountScript(dev, cluster.Name)
		}).(pulumi.StringOutput)
		mountDeps := []pulumi.Resource{installCmd}
		if vol.Attachment != nil {
			mountDeps = append(mountDeps, vol.Attachment)
		}
		mountCmd, err := remote.NewCommand(ctx, name+"-mount", &remote.CommandArgs{
			Connection: conn,
			Create:     mountScript,
			Update:     mountScript,
			Triggers:   pulumi.Array{mountScript},
		}, pulumi.DependsOn(mountDeps))
		if err != nil {
			return nil, fmt.Errorf("database-cluster %q: mount volume: %w", cluster.Name, err)
		}

		// 3) initdb into the PGDATA on the mounted volume (idempotent — a populated
		// volume is never re-initialized).
		initScript := postgres.InitClusterScript(cluster.Name, cluster.Version)
		initCmd, err := remote.NewCommand(ctx, name+"-init", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String(initScript),
			Update:     pulumi.String(initScript),
			Triggers:   pulumi.Array{pulumi.String(initScript)},
		}, pulumi.DependsOn([]pulumi.Resource{mountCmd}))
		if err != nil {
			return nil, fmt.Errorf("database-cluster %q: initdb: %w", cluster.Name, err)
		}

		// 4) Write postgresql.conf/pg_hba.conf + install and (re)start the systemd unit;
		// the listen IP (the host's private IP) is known only at apply.
		applyScript := host.PrivateIP.ApplyT(func(ip string) (string, error) {
			return postgres.ApplyScript(postgres.ClusterConfig{
				Cluster:     cluster.Name,
				Version:     cluster.Version,
				ListenIP:    ip,
				Port:        port,
				NetworkCIDR: cidr,
			})
		}).(pulumi.StringOutput)
		applyCmd, err := remote.NewCommand(ctx, name+"-config", &remote.CommandArgs{
			Connection: conn,
			Create:     applyScript,
			Update:     applyScript,
			Triggers:   pulumi.Array{applyScript},
		}, pulumi.DependsOn([]pulumi.Resource{initCmd}))
		if err != nil {
			return nil, fmt.Errorf("database-cluster %q: configure postgres: %w", cluster.Name, err)
		}

		// 5) Ensure each logical database + its NOLOGIN owner exist, chained after the
		// postmaster is up (and serialized per cluster so two createdb's never race), and
		// register a per-database DBRoleProvisioner that mints per-service roles on-host.
		lastDep := pulumi.Resource(applyCmd)
		for _, db := range databasesOfCluster(res, cluster.Name) {
			dbScript := postgres.EnsureOwnerScript(port, db.Owner) + "\n" + postgres.EnsureDatabaseScript(port, db.Database, db.Owner)
			dbCmd, err := remote.NewCommand(ctx, name+"-db-"+db.Name, &remote.CommandArgs{
				Connection: conn,
				Create:     pulumi.String(dbScript),
				Update:     pulumi.String(dbScript),
				Triggers:   pulumi.Array{pulumi.String(dbScript)},
			}, pulumi.DependsOn([]pulumi.Resource{lastDep}))
			if err != nil {
				return nil, fmt.Errorf("database-cluster %q: database %q: %w", cluster.Name, db.Name, err)
			}
			lastDep = dbCmd
			dbOut[db.Name] = types.DatabaseOutputs{RoleProvisioner: &selfHostedRoleProvisioner{
				conn:      conn,
				privateIP: host.PrivateIP,
				port:      port,
				database:  db.Database,
				owner:     db.Owner,
				dependsOn: dbCmd,
			}}
		}
		// Record this cluster's final command as the host's tail so the next co-located
		// cluster's install waits on it (serializing apt/fstab on the shared host).
		hostLast[hostKey] = lastDep
	}
	return hostLast, nil
}

// selfHostedRoleProvisioner mints a scoped per-service Postgres role on the cluster
// host over SSH (ADR-0036): a private-only cluster is unreachable from the deploy
// machine, so role minting runs on the host via `sudo -u postgres psql` (local peer
// auth). It is the self-hosted DBRoleProvisioner — the retained grant seam — bound to
// one logical database; grants resolve through it exactly as they did for the managed
// provider. dependsOn is the command that created the database + owner, so a role is
// never minted before its database exists.
type selfHostedRoleProvisioner struct {
	conn      remote.ConnectionArgs
	privateIP pulumi.StringOutput
	port      int
	database  string
	owner     string          // the logical database owner: reassign target on an rw→ro downgrade and on role teardown
	dependsOn pulumi.Resource // the command that created the database + owner
	lastMint  pulumi.Resource // the previous role mint on THIS database, to serialize mints
}

// ProvisionRole generates a stable per-service password (random.RandomPassword,
// encrypted in state), mints the LOGIN role + its ro/rw GRANTs on the host, and
// returns the role's connection fields. The password is alphanumeric (Special off) so
// the composed URL needs no percent-encoding. Every published field is gated on the
// mint command so a consumer's secret is written only after the role exists.
func (p *selfHostedRoleProvisioner) ProvisionRole(ctx *pulumi.Context, roleName, permission string) (types.DBRoleFields, error) {
	pw, err := random.NewRandomPassword(ctx, roleName+"-password", &random.RandomPasswordArgs{
		Length:  pulumi.Int(32),
		Special: pulumi.Bool(false),
	})
	if err != nil {
		return types.DBRoleFields{}, fmt.Errorf("db role %q: generate password: %w", roleName, err)
	}
	// The mint script carries the password literal, so build it inside an apply over
	// the secret; the whole command's Create is then encrypted in Pulumi state.
	mintScript := pw.Result.ApplyT(func(password string) (string, error) {
		return postgres.MintRoleScript(p.port, roleName, password, p.database, p.owner, permission)
	}).(pulumi.StringOutput)
	// On teardown (grant/service removed) reassign the role's owned objects to the
	// database owner and drop the role, so a retired service leaves no live login.
	dropScript := postgres.DropRoleScript(p.port, roleName, p.owner)
	// Serialize mints against ONE database: each mint depends on its db-create command
	// AND the previous mint on this database, so concurrent GRANT/ALTER DEFAULT
	// PRIVILEGES sessions never contend on the shared catalog. Different databases hold
	// different provisioners, so their mints still run in parallel. ProvisionRole is
	// called synchronously during program construction, so mutating lastMint is safe.
	mintDeps := []pulumi.Resource{p.dependsOn}
	if p.lastMint != nil {
		mintDeps = append(mintDeps, p.lastMint)
	}
	mintCmd, err := remote.NewCommand(ctx, roleName+"-mint", &remote.CommandArgs{
		Connection: p.conn,
		Create:     mintScript,
		Update:     mintScript,
		Delete:     pulumi.String(dropScript),
		// mintScript embeds the role's random password (secret + unknown at preview);
		// a raw secret in Triggers breaks preview with "malformed RPC secret".
		// safeTrigger hashes + unsecrets it (see program.go).
		Triggers: pulumi.Array{safeTrigger(mintScript)},
		// DeleteBeforeReplace: the default create-before-delete order would
		// mint the new role then immediately DROP it via the old resource's
		// Delete (same roleName), silently leaving a service's granted DB
		// credential missing after a "successful" apply — see program.go's
		// provisionService for the full incident writeup of this bug class.
	}, pulumi.DependsOn(mintDeps), pulumi.DeleteBeforeReplace(true))
	if err != nil {
		return types.DBRoleFields{}, fmt.Errorf("db role %q: mint role: %w", roleName, err)
	}
	p.lastMint = mintCmd
	// gate returns v carrying a dependency on the mint command (via its stdout output),
	// so a field is not consumed before the role is provisioned.
	gate := func(v pulumi.StringInput) pulumi.StringOutput {
		return pulumi.All(mintCmd.Stdout, v).ApplyT(func(vs []interface{}) string {
			return vs[1].(string)
		}).(pulumi.StringOutput)
	}
	url := pulumi.Sprintf("postgresql://%s:%s@%s:%d/%s", roleName, pw.Result, p.privateIP, p.port, p.database)
	return types.DBRoleFields{
		User:     gate(pulumi.String(roleName)),
		Password: gate(pw.Result),
		Host:     gate(p.privateIP),
		Port:     gate(pulumi.String(strconv.Itoa(p.port))),
		DBName:   gate(pulumi.String(p.database)),
		URL:      gate(url),
	}, nil
}
