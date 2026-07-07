package program

import (
	"fmt"
	"time"

	"github.com/pulumi/pulumi-command/sdk/go/command/remote"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/wardnet/inforge/internal/dbbackup"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/postgres"
	iremote "github.com/wardnet/inforge/internal/remote"
	"github.com/wardnet/inforge/internal/types"
)

// provisionDatabaseBackups installs the per-database backup timer on each cluster
// host in one scope (ADR-0036). For every logical database whose backup policy is
// enabled it renders a `pg_dump -Fc | gzip` → R2 upload driven by a systemd timer at
// the database's interval, keeping the newest `keep` objects. Per host it installs
// awscli once and writes the shared backup-scoped R2 credential (both delivered
// decrypted, the credential encrypted in Pulumi state), then one timer per enabled
// database.
//
// It runs AFTER provisionDatabaseClusters and DependsOn its per-host tail
// (dbHostTails) so the awscli apt install never races the Postgres apt install on the
// same host, and a timer is installed only after its cluster is up. It makes NO
// firewall change — backups are outbound HTTPS to R2 only.
//
// bucket/endpoint come from stack config (setBackups, derived from inforge.yaml
// backups.bucket + CLOUDFLARE_ACCOUNT_ID). accessKey/secretKey are the backup-scoped
// R2 credential, decrypted once from the reserved secret namespace and marked secret
// by the caller; credsPresent reports whether both were actually found in the store.
//
// The two guardrails are symmetric and only fire when this scope has ≥1 enabled backup
// database: a missing bucket is the data-unprotected trap (slice 3 ships backups
// enabled by default, so it must fail loudly, not silently skip), and a set bucket with
// a missing credential fails too — but a scope whose databases all opt out requires
// neither, so a fleet-wide backups.bucket never forces an unused credential on it.
//
// regionLabel (the scope slug, or "global") is the second segment of every R2 object
// key, so a regional cluster deployed under one name to many regions keeps each
// region's backups — and its retention prune — separate.
func provisionDatabaseBackups(ctx *pulumi.Context, res types.Resources, computeOut map[string]types.ComputeOutputs, dbHostTails map[string]pulumi.Resource, bucket, endpoint string, credsPresent bool, accessKey, secretKey pulumi.StringOutput, deployPrivateKey, env, slug string) error {
	if len(res.DatabaseCluster) == 0 {
		return nil
	}
	regionLabel := slug
	if regionLabel == "" {
		regionLabel = "global"
	}
	canonical := naming.CanonicalComputeKeys(res.Compute)
	deployUserByCompute := naming.DeployUsersByHost(res.Compute)
	ports := clusterPortsByHost(res, canonical)

	// item is one enabled logical database's backup on its host.
	type item struct {
		cluster  string
		database string
		port     int
		interval time.Duration
		keep     int
	}
	byHost := map[string][]item{}
	total := 0
	for _, cluster := range res.DatabaseCluster {
		hostKey, ok := canonical[cluster.Host]
		if !ok {
			continue // an unresolved host FK is reported by validation
		}
		for _, db := range databasesOfCluster(res, cluster.Name) {
			if db.Backup.Enabled != nil && !*db.Backup.Enabled {
				continue // explicit opt-out
			}
			// Interval is loader-defaulted and validate-checked, so a parse error here is
			// a defence-in-depth guard, not the primary check.
			interval, err := time.ParseDuration(db.Backup.Interval)
			if err != nil {
				return fmt.Errorf("backups: database %q: backup.interval %q: %w", db.Name, db.Backup.Interval, err)
			}
			byHost[hostKey] = append(byHost[hostKey], item{
				cluster:  cluster.Name,
				database: db.Database,
				port:     ports[hostKey][cluster.Name],
				interval: interval,
				keep:     db.Backup.Keep,
			})
			total++
		}
	}
	if total == 0 {
		return nil // every database opted out — nothing to deliver
	}
	if bucket == "" {
		return fmt.Errorf("backups: %d database(s) default to backups enabled but inforge.yaml declares no backups.bucket — set backups.bucket (distinct from the state and artifacts buckets) or set backup.enabled: false on those databases", total)
	}
	if !credsPresent && !ctx.DryRun() {
		return fmt.Errorf("backups: %d database(s) have backups enabled and backups.bucket is set, but secrets.enc.yaml is missing the %s/%s and %s/%s R2 credential — run `inforge secret set <env> %s %s --reserved` and `inforge secret set <env> %s %s --reserved`, then commit the store", total, dbbackup.AuthSecretNamespace, dbbackup.AccessKeyIDKey, dbbackup.AuthSecretNamespace, dbbackup.SecretAccessKeyKey, dbbackup.AuthSecretNamespace, dbbackup.AccessKeyIDKey, dbbackup.AuthSecretNamespace, dbbackup.SecretAccessKeyKey)
	}

	// The R2 credential is the same for every host; build the write script once inside
	// an apply over the two secret values so the command's Create is encrypted in state.
	credScript := pulumi.All(accessKey, secretKey).ApplyT(func(v []interface{}) string {
		return dbbackup.CredentialScript(v[0].(string), v[1].(string))
	}).(pulumi.StringOutput)

	for _, hostKey := range sortedKeys(byHost) {
		host, ok := computeOut[hostKey]
		if !ok {
			return fmt.Errorf("backups: host %q has no compute output", hostKey)
		}
		deployUser := deployUserByCompute[hostKey]
		if !ctx.DryRun() {
			if deployUser == "" {
				return fmt.Errorf("backups: host %q has no deploy_user; inforge needs one to SSH and install the backup timers", hostKey)
			}
			if deployPrivateKey == "" {
				return fmt.Errorf("backups: no deploy private key configured (set the deploy_private_key stack config or INFORGE_DEPLOY_PRIVATE_KEY)")
			}
		}
		conn := iremote.Connection(host.PublicIP, deployUser, deployPrivateKey)
		name := naming.Resource(env, slug, "dbbackup", hostKey)

		// Install awscli, serialized after this host's cluster provisioning so co-located
		// apt runs never race the dpkg lock.
		var setupDeps []pulumi.Resource
		if tail := dbHostTails[hostKey]; tail != nil {
			setupDeps = append(setupDeps, tail)
		}
		install := dbbackup.InstallScript()
		installCmd, err := remote.NewCommand(ctx, name+"-install", &remote.CommandArgs{
			Connection: conn,
			Create:     pulumi.String(install),
			Update:     pulumi.String(install),
			Triggers:   pulumi.Array{pulumi.String(install)},
		}, pulumi.DependsOn(setupDeps))
		if err != nil {
			return fmt.Errorf("backups: host %q: install awscli: %w", hostKey, err)
		}

		credCmd, err := remote.NewCommand(ctx, name+"-credential", &remote.CommandArgs{
			Connection: conn,
			Create:     credScript,
			Update:     credScript,
			Triggers:   pulumi.Array{credScript},
		}, pulumi.DependsOn([]pulumi.Resource{installCmd}))
		if err != nil {
			return fmt.Errorf("backups: host %q: write R2 credential: %w", hostKey, err)
		}

		for _, it := range byHost[hostKey] {
			cfg := dbbackup.BackupConfig{
				Env:         env,
				Region:      regionLabel,
				Cluster:     it.cluster,
				Database:    it.database,
				Port:        it.port,
				ClusterUnit: postgres.ServiceName(it.cluster),
				Bucket:      bucket,
				Endpoint:    endpoint,
				Keep:        it.keep,
				Interval:    it.interval,
			}
			applyScript, err := dbbackup.ApplyScript(cfg)
			if err != nil {
				return fmt.Errorf("backups: database %q: %w", it.database, err)
			}
			removeScript := dbbackup.RemoveScript(it.cluster, it.database)
			// A database opting out (removed from byHost on a later deploy) drops this
			// resource, so Pulumi runs the Delete → RemoveScript, tearing the timer down.
			if _, err := remote.NewCommand(ctx, name+"-"+it.cluster+"-"+it.database, &remote.CommandArgs{
				Connection: conn,
				Create:     pulumi.String(applyScript),
				Update:     pulumi.String(applyScript),
				Delete:     pulumi.String(removeScript),
				Triggers:   pulumi.Array{pulumi.String(applyScript)},
			}, pulumi.DependsOn([]pulumi.Resource{credCmd})); err != nil {
				return fmt.Errorf("backups: database %q: install timer: %w", it.database, err)
			}
		}
	}
	return nil
}
