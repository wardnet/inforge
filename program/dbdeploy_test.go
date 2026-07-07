package program

import (
	"testing"

	"github.com/wardnet/inforge/internal/regions"
	"github.com/wardnet/inforge/internal/types"
)

func ptrBool(b bool) *bool { return &b }

// TestBuildDBDeployDescriptor covers the load-bearing properties: a regional
// cluster fans one target per region with the right region slug; the global
// scope drops the region segment; the port comes from clusterPortsByHost; and a
// backup-disabled database is still enumerated (with BackupEnabled=false).
func TestBuildDBDeployDescriptor(t *testing.T) {
	deployUser := &types.DeployUserSpec{Name: "deployer"}
	regional := types.Resources{
		Compute: []types.ComputeSpec{
			{Name: "db", InstanceCount: 1, DeployUser: deployUser},
		},
		DatabaseCluster: []types.DatabaseClusterSpec{
			{Name: "pg", Host: "db", Provider: string(types.SelfHostedProvider)},
		},
		Database: []types.DatabaseSpec{
			{Name: "app", Cluster: "pg", Database: "appdb", Owner: "appowner"},
			{Name: "tmp", Cluster: "pg", Database: "tmpdb", Owner: "appowner", Backup: types.BackupPolicy{Enabled: ptrBool(false)}},
		},
	}
	global := types.Resources{}
	table := regions.Table{
		"us-east": {Slug: "use1"},
		"eu-west": {Slug: "euc1"},
	}

	desc, err := buildDBDeployDescriptor("prd", "example.com", regional, global, table)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// 2 databases × 2 regions = 4 targets (global scope has no clusters).
	if len(desc.Targets) != 4 {
		t.Fatalf("want 4 targets, got %d: %+v", len(desc.Targets), desc.Targets)
	}

	// Index by (database, regionSlug) for assertions.
	byKey := map[string]struct {
		slug, host, user string
		port             int
		enabled          bool
	}{}
	for _, tgt := range desc.Targets {
		byKey[tgt.Database+"@"+tgt.RegionSlug] = struct {
			slug, host, user string
			port             int
			enabled          bool
		}{tgt.RegionSlug, tgt.HostDNS, tgt.SSHUser, tgt.Port, tgt.BackupEnabled}
	}

	appUse1, ok := byKey["appdb@use1"]
	if !ok {
		t.Fatalf("missing appdb@use1: %+v", desc.Targets)
	}
	if appUse1.user != "deployer" {
		t.Errorf("ssh user = %q, want deployer", appUse1.user)
	}
	if appUse1.port == 0 {
		t.Errorf("port not set from clusterPortsByHost")
	}
	if !appUse1.enabled {
		t.Errorf("appdb should default to backup-enabled")
	}
	if _, ok := byKey["appdb@euc1"]; !ok {
		t.Errorf("appdb should fan to the euc1 region too")
	}

	// The disabled-backup database is still enumerated, flagged disabled.
	tmp, ok := byKey["tmpdb@use1"]
	if !ok {
		t.Fatalf("tmpdb (backups disabled) must still be enumerated for restore/list")
	}
	if tmp.enabled {
		t.Errorf("tmpdb has backup.enabled:false — BackupEnabled should be false")
	}
}
