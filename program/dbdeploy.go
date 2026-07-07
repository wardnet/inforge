package program

import (
	"fmt"

	"github.com/wardnet/inforge/internal/dbbackup"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

// dbDefaultSSHUser mirrors the service/app/mesh deploy descriptors' fallback:
// hosts with no declared deploy_user are provisioned with "deploy".
const dbDefaultSSHUser = "deploy"

// buildDBDeployDescriptor derives the `dbDeployDescriptor` stack output: one
// target per logical database, across every region plus the global scope (the
// database sibling of the service/app/mesh deploy descriptors). It is the
// contract the `inforge db backup | list-backups | restore` commands resolve a
// database's cluster host, SSH user, TCP port, and R2 region segment from.
//
// The port is NOT re-derived here: it reuses clusterPortsByHost — the same
// host-sorted assignment the cluster realization, the backup timer, and
// postgresql.conf agree on — so the descriptor's port can never drift from the
// port a backup was actually taken on. Every logical database is enumerated
// (backup-enabled or not) so restore/list-backups can address an opted-out
// database; BackupEnabled is carried as data for `db backup` to filter on.
func buildDBDeployDescriptor(env, baseDomain string, regional, global types.Resources, table regions.Table) (dbbackup.DeployDescriptor, error) {
	desc := dbbackup.DeployDescriptor{Environment: env}

	appendScope := func(res types.Resources, slug string) {
		canonical := naming.CanonicalComputeKeys(res.Compute)
		deployUsers := naming.DeployUsersByHost(res.Compute)
		ports := clusterPortsByHost(res, canonical)
		// regionSlug is the R2 key's region segment: the scope slug, or "global"
		// for the region-less global scope (matches dbbackup.ObjectPrefix and the
		// backup timer's own key).
		regionSlug := slug
		if regionSlug == "" {
			regionSlug = "global"
		}
		for _, cluster := range res.DatabaseCluster {
			hostKey, ok := canonical[cluster.Host]
			if !ok {
				continue // an unresolved host FK is reported by validation
			}
			sshUser := deployUsers[hostKey]
			if sshUser == "" {
				sshUser = dbDefaultSSHUser
			}
			for _, db := range databasesOfCluster(res, cluster.Name) {
				enabled := db.Backup.Enabled == nil || *db.Backup.Enabled
				desc.Targets = append(desc.Targets, dbbackup.DeployTarget{
					Cluster:       cluster.Name,
					Database:      db.Database,
					HostDNS:       naming.HostFQDN(env, slug, naming.BareComputeName(hostKey), baseDomain),
					SSHUser:       sshUser,
					Port:          ports[hostKey][cluster.Name],
					RegionSlug:    regionSlug,
					BackupEnabled: enabled,
				})
			}
		}
	}

	for _, region := range sortedKeys(table) {
		slug, err := table.Slug(region)
		if err != nil {
			return dbbackup.DeployDescriptor{}, fmt.Errorf("region %q: %w", region, err)
		}
		appendScope(regional, slug)
	}
	// The global slice is region-less: empty slug drops the region segment from the
	// host FQDN, matching the global host's DNS record.
	appendScope(global, "")

	return desc, nil
}
