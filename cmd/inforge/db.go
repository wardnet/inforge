package main

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/backupstore"
	"github.com/wardnet/inforge/internal/dbbackup"
	"github.com/wardnet/inforge/internal/remote"
)

// `inforge db` — the operator surface over the self-hosted Postgres backups
// (ADR-0036). All three subcommands resolve their targets from the
// `dbDeployDescriptor` stack output (the database sibling of the service/app/mesh
// descriptors) and are signal-push over SSH — `backup` triggers the on-host
// backup oneshot, `restore` runs an on-host pg_restore; neither ships material or
// runs Pulumi. `list-backups` reads R2 directly with the operator's own creds.

func newDBCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Operate on self-hosted Postgres backups (trigger, list, restore)",
	}
	cmd.AddCommand(
		newDBBackupCmd(configPath),
		newDBListBackupsCmd(configPath),
		newDBRestoreCmd(configPath),
	)
	return cmd
}

// resolveDBTargets reads every logical-database target from the dbDeployDescriptor
// stack output for env.
func resolveDBTargets(ctx context.Context, projCfg projectConfig, env, stackConfigPath string) ([]dbbackup.DeployTarget, error) {
	stackCfg, err := resolveStackConfig(stackConfigPath, env)
	if err != nil {
		return nil, err
	}
	targets, err := resolveDescriptorTargets(ctx, projCfg, env, "dbDeployDescriptor", stackCfg,
		func(dbbackup.DeployTarget) bool { return true })
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no databases found in dbDeployDescriptor for env %q — is a database-cluster + database defined, and has `inforge deploy` been run?", env)
	}
	return targets, nil
}

// filterDBTargets narrows targets to an optional database name, cluster name, and
// region slug ("" matches all). Cluster is a discriminator because a logical
// database name is only unique per (cluster, database) — two clusters in one scope
// may each own a database of the same name — so restoring one requires naming the
// cluster too.
func filterDBTargets(targets []dbbackup.DeployTarget, database, cluster, region string) []dbbackup.DeployTarget {
	var out []dbbackup.DeployTarget
	for _, t := range targets {
		if database != "" && t.Database != database {
			continue
		}
		if cluster != "" && t.Cluster != cluster {
			continue
		}
		if region != "" && t.RegionSlug != region {
			continue
		}
		out = append(out, t)
	}
	return out
}

// noTargetsError builds the "nothing matched" error for a filter, naming only the
// discriminators the operator actually supplied — so a mistyped --region on a
// list-all invocation is not misreported as a missing (empty-named) database.
func noTargetsError(env, database, cluster, region string) error {
	var quals []string
	if database != "" {
		quals = append(quals, fmt.Sprintf("database %q", database))
	}
	if cluster != "" {
		quals = append(quals, fmt.Sprintf("cluster %q", cluster))
	}
	if region != "" {
		quals = append(quals, fmt.Sprintf("region %q", region))
	}
	if len(quals) == 0 {
		return fmt.Errorf("no databases found for env %q", env)
	}
	return fmt.Errorf("no database matching %s found for env %q", strings.Join(quals, ", "), env)
}

// dbAccount is the SSH connect string for a target (deploy_user@host_dns).
func dbAccount(t dbbackup.DeployTarget) string { return t.SSHUser + "@" + t.HostDNS }

// newBackupStore builds the R2 lister over the configured backups bucket, using
// the operator's own AWS_* credentials (the CLI has full R2 access; the host has
// only the backup-scoped credential).
func newBackupStore(ctx context.Context, projCfg projectConfig) (*backupstore.Store, error) {
	if !projCfg.Backups.configured() {
		return nil, fmt.Errorf("no backups bucket configured — add a `backups:` block to inforge.yaml")
	}
	return backupstore.NewStore(ctx, projCfg.Backups.Bucket, os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
}

// --- inforge db backup --------------------------------------------------------

func newDBBackupCmd(configPath *string) *cobra.Command {
	var cluster, region, stackConfig, sshKeyPath string
	cmd := &cobra.Command{
		Use:   "backup <env> [<database>]",
		Short: "Trigger an on-host backup now for one or all databases",
		Long: "SSH-trigger the on-host backup oneshot (a `pg_dump -Fc | gzip` upload to R2)\n" +
			"for the named database, or every backup-enabled database when omitted. Fans out\n" +
			"across all regions by default; --region narrows to one, --cluster to one cluster.\n" +
			"Databases with backup.enabled:false have no backup unit and are skipped.",
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			database := ""
			if len(args) == 2 {
				database = args[1]
			}
			return runDBBackup(cmd.Context(), *configPath, args[0], database, cluster, region, stackConfig, sshKeyPath)
		},
	}
	cmd.Flags().StringVar(&cluster, "cluster", "", "restrict to one database-cluster (default: all clusters)")
	cmd.Flags().StringVar(&region, "region", "", "restrict to one region slug (default: all regions)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	return cmd
}

func runDBBackup(ctx context.Context, configPath, env, database, cluster, region, stackConfigPath, sshKeyPath string) error {
	key, err := resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	all, err := resolveDBTargets(ctx, projCfg, env, stackConfigPath)
	if err != nil {
		return err
	}
	targets := filterDBTargets(all, database, cluster, region)
	if len(targets) == 0 {
		return noTargetsError(env, database, cluster, region)
	}
	// Only backup-enabled databases have a backup unit to start.
	var enabled []dbbackup.DeployTarget
	for _, t := range targets {
		if t.BackupEnabled {
			enabled = append(enabled, t)
		}
	}
	if len(enabled) == 0 {
		if database != "" {
			return fmt.Errorf("database %q has backups disabled (backup.enabled:false) — no backup unit to trigger; enable backups and redeploy, or use `inforge db restore --from-dump` for a throwaway database", database)
		}
		return fmt.Errorf("no backup-enabled databases matched for env %q (cluster %q, region %q)", env, cluster, region)
	}

	var failures int
	for _, t := range enabled {
		unit := dbbackup.ServiceName(t.Cluster, t.Database)
		fmt.Printf("→ %s/%s (%s): triggering %s\n", t.Cluster, t.Database, t.RegionSlug, unit)
		// systemctl start on a oneshot blocks until the dump completes and exits
		// non-zero if it failed, so a failed trigger surfaces the real error.
		if err := sshRun(ctx, key, dbAccount(t), "sudo systemctl start "+remote.Quote(unit), os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s/%s (%s): %v\n", t.Cluster, t.Database, t.RegionSlug, err)
			failures++
			continue
		}
		fmt.Printf("  ✓ %s/%s (%s): backup complete\n", t.Cluster, t.Database, t.RegionSlug)
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d backup trigger(s) failed", failures, len(enabled))
	}
	return nil
}

// --- inforge db list-backups --------------------------------------------------

func newDBListBackupsCmd(configPath *string) *cobra.Command {
	var cluster, region, stackConfig string
	cmd := &cobra.Command{
		Use:   "list-backups <env> [<database>]",
		Short: "List R2 backups for one or all databases",
		Long: "List the backup archives in R2 for the named database (or every database when\n" +
			"omitted), grouped by region and database. --region / --cluster narrow the set.\n" +
			"Reads R2 with the operator's own AWS_* credentials.",
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			database := ""
			if len(args) == 2 {
				database = args[1]
			}
			return runDBListBackups(cmd.Context(), *configPath, args[0], database, cluster, region, stackConfig)
		},
	}
	cmd.Flags().StringVar(&cluster, "cluster", "", "restrict to one database-cluster (default: all clusters)")
	cmd.Flags().StringVar(&region, "region", "", "restrict to one region slug (default: all regions)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	return cmd
}

func runDBListBackups(ctx context.Context, configPath, env, database, cluster, region, stackConfigPath string) error {
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	all, err := resolveDBTargets(ctx, projCfg, env, stackConfigPath)
	if err != nil {
		return err
	}
	targets := filterDBTargets(all, database, cluster, region)
	if len(targets) == 0 {
		return noTargetsError(env, database, cluster, region)
	}
	store, err := newBackupStore(ctx, projCfg)
	if err != nil {
		return err
	}
	sortDBTargets(targets)
	for _, t := range targets {
		prefix := dbbackup.ObjectPrefix(env, t.RegionSlug, t.Cluster, t.Database)
		objs, err := store.List(ctx, prefix)
		if err != nil {
			return err
		}
		fmt.Printf("%s/%s (%s) — %d backup(s):\n", t.Cluster, t.Database, t.RegionSlug, len(objs))
		if len(objs) == 0 {
			fmt.Println("  (none)")
			continue
		}
		for _, o := range objs {
			fmt.Printf("  %s  %10d bytes\n", o.Key, o.Size)
		}
	}
	return nil
}

// --- inforge db restore -------------------------------------------------------

func newDBRestoreCmd(configPath *string) *cobra.Command {
	var fromKey, fromDump, cluster, region, stackConfig, sshKeyPath string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <env> <database>",
		Short: "Restore a database from an R2 backup or a local dump (data-only)",
		Long: "SSH-trigger an on-host pg_restore of a database. By default restores the newest\n" +
			"R2 backup; --from-key restores a specific key; --from-dump uploads a local\n" +
			"pg_dump -Fc file and restores it (no R2 needed). Restore is DATA-ONLY — roles are\n" +
			"re-minted by `inforge deploy`, so run a deploy afterwards to re-apply grants.\n\n" +
			"Restore is destructive (pg_restore --clean drops and recreates objects). Without\n" +
			"--yes it prints the resolved plan and exits without touching the database. When a\n" +
			"database resolves to more than one target, pass --region and/or --cluster to pick\n" +
			"one; --from-key names both, so neither flag is needed with it.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDBRestore(cmd.Context(), *configPath, args[0], args[1], fromKey, fromDump, cluster, region, stackConfig, sshKeyPath, yes)
		},
	}
	cmd.Flags().StringVar(&fromKey, "from-key", "", "restore a specific R2 backup key (default: newest)")
	cmd.Flags().StringVar(&fromDump, "from-dump", "", "restore a local pg_dump -Fc file (uploaded to the host; no R2)")
	cmd.Flags().StringVar(&cluster, "cluster", "", "database-cluster (required when the database name repeats across clusters)")
	cmd.Flags().StringVar(&region, "region", "", "region slug (required when the database spans multiple regions)")
	cmd.Flags().BoolVar(&yes, "yes", false, "execute the restore (without this flag, only the plan is printed)")
	cmd.Flags().StringVar(&stackConfig, "stack-config", "", "path to the infra stack config file (default: inforge.<env>.yaml)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "path to the SSH deploy key (overrides INFORGE_DEPLOY_KEY)")
	return cmd
}

func runDBRestore(ctx context.Context, configPath, env, database, fromKey, fromDump, cluster, region, stackConfigPath, sshKeyPath string, yes bool) error {
	if fromKey != "" && fromDump != "" {
		return fmt.Errorf("--from-key and --from-dump are mutually exclusive")
	}
	projCfg, err := loadProjectConfig(configPath)
	if err != nil {
		return err
	}
	all, err := resolveDBTargets(ctx, projCfg, env, stackConfigPath)
	if err != nil {
		return err
	}

	// --from-key self-disambiguates: the key names env/region/cluster/database, so
	// derive region AND cluster from it (validating they match the args/flags). This
	// is the single-target guarantee's load-bearing step — the key's cluster segment
	// must be honoured, or a key from cluster A could restore into cluster B's
	// same-named database.
	if fromKey != "" {
		keyEnv, keyRegion, keyCluster, keyDB, _, err := dbbackup.ParseObjectKey(fromKey)
		if err != nil {
			return fmt.Errorf("--from-key: %w", err)
		}
		if keyEnv != env {
			return fmt.Errorf("--from-key env segment %q does not match env %q", keyEnv, env)
		}
		if keyDB != database {
			return fmt.Errorf("--from-key database segment %q does not match database %q", keyDB, database)
		}
		if region != "" && region != keyRegion {
			return fmt.Errorf("--region %q conflicts with the region in --from-key (%q)", region, keyRegion)
		}
		if cluster != "" && cluster != keyCluster {
			return fmt.Errorf("--cluster %q conflicts with the cluster in --from-key (%q)", cluster, keyCluster)
		}
		region, cluster = keyRegion, keyCluster
	}

	candidates := filterDBTargets(all, database, cluster, region)
	if len(candidates) == 0 {
		return noTargetsError(env, database, cluster, region)
	}
	if len(candidates) > 1 {
		return fmt.Errorf("database %q resolves to %d targets (%s) — pass --region and/or --cluster to pick exactly one", database, len(candidates), strings.Join(targetLabels(candidates), ", "))
	}
	target := candidates[0]

	// Resolve the source and a human description of it.
	var source string
	switch {
	case fromDump != "":
		if _, err := os.Stat(fromDump); err != nil {
			return fmt.Errorf("--from-dump: %w", err)
		}
		source = "local dump " + fromDump
	case fromKey != "":
		source = "R2 key " + fromKey
	default:
		store, err := newBackupStore(ctx, projCfg)
		if err != nil {
			return err
		}
		prefix := dbbackup.ObjectPrefix(env, target.RegionSlug, target.Cluster, target.Database)
		objs, err := store.List(ctx, prefix)
		if err != nil {
			return err
		}
		if len(objs) == 0 {
			return fmt.Errorf("no backups found under %s — nothing to restore (take one with `inforge db backup %s %s`)", prefix, env, database)
		}
		fromKey = objs[len(objs)-1].Key // newest (keys sort chronologically)
		source = "R2 key " + fromKey + " (newest)"
	}

	fmt.Printf("Restore plan:\n")
	fmt.Printf("  database : %s (logical: %s)\n", database, target.Database)
	fmt.Printf("  cluster  : %s\n", target.Cluster)
	fmt.Printf("  region   : %s\n", target.RegionSlug)
	fmt.Printf("  host     : %s (port %d)\n", target.HostDNS, target.Port)
	fmt.Printf("  source   : %s\n", source)
	if !yes {
		fmt.Println("\nDry run — pass --yes to execute. This is DESTRUCTIVE (pg_restore --clean drops and recreates objects).")
		return nil
	}

	sshKey, err := resolveSSHKey(sshKeyPath)
	if err != nil {
		return err
	}
	cfg := dbbackup.RestoreConfig{Port: target.Port, Database: target.Database}
	account := dbAccount(target)

	if fromDump != "" {
		remotePath := dbbackup.RemoteRestorePath(target.Cluster, target.Database)
		fmt.Printf("uploading %s → %s:%s...\n", fromDump, target.HostDNS, remotePath)
		if err := streamGzippedFile(ctx, sshKey, account, remotePath, fromDump); err != nil {
			return err
		}
		script, err := dbbackup.RestoreFromFileScript(cfg, remotePath)
		if err != nil {
			return err
		}
		fmt.Printf("restoring on %s...\n", target.HostDNS)
		return sshRunScriptAsRoot(ctx, sshKey, account, script, os.Stdout)
	}

	bucket, endpoint, err := projCfg.Backups.resolve()
	if err != nil {
		return err
	}
	script, err := dbbackup.RestoreFromKeyScript(cfg, bucket, endpoint, fromKey)
	if err != nil {
		return err
	}
	fmt.Printf("restoring on %s...\n", target.HostDNS)
	return sshRunScriptAsRoot(ctx, sshKey, account, script, os.Stdout)
}

// targetLabels returns sorted "cluster/region" labels for a target set, for the
// ambiguity error — so an operator sees exactly which (cluster, region) pairs a
// database name resolved to and which flag disambiguates them.
func targetLabels(targets []dbbackup.DeployTarget) []string {
	labels := make([]string, 0, len(targets))
	for _, t := range targets {
		labels = append(labels, t.Cluster+"/"+t.RegionSlug)
	}
	sort.Strings(labels)
	return labels
}

// sortDBTargets orders targets by (region, cluster, database) for stable output.
func sortDBTargets(targets []dbbackup.DeployTarget) {
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.RegionSlug != b.RegionSlug {
			return a.RegionSlug < b.RegionSlug
		}
		if a.Cluster != b.Cluster {
			return a.Cluster < b.Cluster
		}
		return a.Database < b.Database
	})
}

// streamGzippedFile gzips localPath on the fly and streams it to remotePath on the
// host (the --from-dump transport). The renderer's restore pipeline gunzips the
// file, so the on-the-fly gzip matches the backup format; nothing is buffered to
// disk or fully into memory.
func streamGzippedFile(ctx context.Context, keyPath, account, remotePath, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	pr, pw := io.Pipe()
	// Own the read end: if sshWriteFileFrom returns early (SSH auth failure, host
	// reset mid-transfer), the exec stops draining pr, and without closing it the
	// producer goroutine would block forever on pw.Write (io.Pipe writes block until
	// read). Closing pr makes that pending write return io.ErrClosedPipe, unwinding
	// the goroutine and releasing the file.
	defer func() { _ = pr.Close() }()
	go func() {
		gz := gzip.NewWriter(pw)
		if _, err := io.Copy(gz, f); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if err := gz.Close(); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return sshWriteFileFrom(ctx, keyPath, account, remotePath, pr)
}

// sshRunScriptAsRoot runs a full rendered script as root on the host: it
// base64-encodes the script and pipes it into `sudo bash` over SSH. The restore
// scripts read the root-owned backup credential and drop to the postgres user, so
// they must run as root; encoding avoids any shell-quoting of the multi-line body.
func sshRunScriptAsRoot(ctx context.Context, keyPath, account, script string, w io.Writer) error {
	enc := base64.StdEncoding.EncodeToString([]byte(script))
	remoteCmd := "printf %s " + remote.Quote(enc) + " | base64 -d | sudo bash"
	return sshRun(ctx, keyPath, account, remoteCmd, w)
}
