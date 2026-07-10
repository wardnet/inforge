package dbbackup

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// DeployTarget is one logical database's deployment coordinates the `inforge db`
// commands resolve from the `dbDeployDescriptor` stack output — enough to SSH the
// cluster host and to address the database's backups in R2. It enumerates EVERY
// logical database (all clusters × all regions), not only backup-enabled ones:
// `db restore` and `db list-backups` must be able to target a database whose
// backups are disabled (the `--from-dump` seed/migration case), so BackupEnabled
// is carried as data rather than a filter applied at descriptor-build time.
type DeployTarget struct {
	// Cluster is the database-cluster resource name; Database is the logical
	// Postgres database name (the pg_dump/pg_restore target and the R2 key segment).
	Cluster  string `yaml:"cluster" json:"cluster" pulumi:"cluster"`
	Database string `yaml:"database" json:"database" pulumi:"database"`
	// HostDNS is the host's SSH/cloud-init DNS name; SSHUser is the account inforge
	// connects as.
	HostDNS string `yaml:"host_dns" json:"host_dns" pulumi:"hostDns"`
	SSHUser string `yaml:"ssh_user" json:"ssh_user" pulumi:"sshUser"`
	// Port is the cluster's TCP port on its host (postgres.ClusterPort), needed for
	// the on-host pg_restore. RegionSlug is the R2 key's region segment (the region
	// slug, or "global" for the global scope).
	Port       int    `yaml:"port" json:"port" pulumi:"port"`
	RegionSlug string `yaml:"region_slug" json:"region_slug" pulumi:"regionSlug"`
	// BackupEnabled reports whether this database has a backup timer (its
	// `backup.enabled` is not explicitly false). `db backup` operates only on
	// enabled targets; restore/list-backups ignore it.
	BackupEnabled bool `yaml:"backup_enabled" json:"backup_enabled" pulumi:"backupEnabled"`
}

// DeployDescriptor is the `dbDeployDescriptor` stack output (the database sibling
// of deployDescriptor / appDeployDescriptor / meshDeployDescriptor). It is derived
// purely from resolved resources — no cloud outputs.
//
// Every field MUST also carry a `pulumi:"..."` tag (in addition to yaml/json):
// this type is exported via ctx.Export(name, pulumi.Any(desc)), and the Pulumi
// Go SDK's reflection-based struct marshaler (marshalInputOptionsImpl,
// go/pulumi/rpc.go) silently drops any field with no `pulumi` tag — yaml/json
// tags alone produce an empty {} export. This broke all four *DeployDescriptor
// stack outputs in production (deployDescriptor/appDeployDescriptor/
// meshDeployDescriptor/dbDeployDescriptor all exported as {}).
type DeployDescriptor struct {
	Environment string         `yaml:"environment" json:"environment" pulumi:"environment"`
	Targets     []DeployTarget `yaml:"targets" json:"targets" pulumi:"targets"`
}

// Marshal renders the descriptor as YAML (mirrors the sibling descriptors).
func (d DeployDescriptor) Marshal() ([]byte, error) {
	b, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal db deploy descriptor: %w", err)
	}
	return b, nil
}
