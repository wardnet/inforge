package output

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/wardnet/inforge/internal/naming"
)

// opInfo is the human translation of one resource operation: what the resource
// IS (kind + subject + scope) and — when the operation destroys something on a
// host — what exactly is destroyed. It is derived from the resource-name
// grammar every inforge resource follows (`wardnet-<env>[-<slug>]-<type>-<rest>`
// plus the per-command suffixes), so the deploy output can say "service unit
// tenants (use1)" instead of "command:remote:Command wardnet-prd-use1-svc-
// tenants-provision".
type opInfo struct {
	kind    string // short noun: "service unit", "db role", "server", ...
	subject string // the resource's own name: "tenants", "bridge-01", ...
	scope   string // region slug, or "" (global / not parseable)
	// destroys says what a DELETE of this resource does on the host — shown in
	// the destructive-operations section. Empty means the delete is not known
	// to destroy host state (or the op is not a delete).
	destroys string
	// suppressOnReplace keeps the ⚠ off OpDeleteReplaced for kinds whose
	// trigger-driven replace is the ROUTINE change path (the delete-free
	// -secrets command post-ADR-0042): warning on every secret rotation would
	// train operators to ignore the section. True removal (OpDelete) always
	// warns.
	suppressOnReplace bool
}

// display renders "kind subject (scope)" with the scope omitted when unknown.
func (i opInfo) display() string {
	s := i.kind
	if i.subject != "" {
		s += " " + i.subject
	}
	if i.scope != "" {
		s += " (" + i.scope + ")"
	}
	return s
}

// translate parses a Pulumi resource name (plus its type token) into the human
// opInfo. The grammar decode lives beside the name builders
// (naming.ParseResourceName), so the parser and the builders cannot drift;
// this layer only maps the decoded parts onto human kinds and destroy
// descriptions. Unrecognized names fall back to the short type + raw name, so
// the output never hides a resource it cannot explain.
func translate(typ, name string) opInfo {
	grammarType, rest, slug, ok := naming.ParseResourceName(name)
	if !ok {
		return opInfo{kind: shortType(typ), subject: name}
	}
	return classify(grammarType, rest, slug)
}

// classify maps a (type token, remaining name) pair onto its human kind, its
// subject, and — where the delete script destroys host state — what it destroys.
func classify(typ, tail, scope string) opInfo {
	i := opInfo{kind: typ, subject: tail, scope: scope}
	cut := func(suffix string) (string, bool) { return strings.CutSuffix(tail, suffix) }
	switch typ {
	case "svc":
		if s, ok := cut("-provision"); ok {
			return opInfo{kind: "service unit", subject: s, scope: scope,
				destroys: fmt.Sprintf("stops service %q and removes its unit, descriptor.yaml and secrets.age", s)}
		}
		if s, ok := cut("-secrets"); ok {
			return opInfo{kind: "service config+secrets", subject: s, scope: scope,
				// destroys describes the worst case: a delete script recorded by a
				// pre-ADR-0042 deploy. Post-migration the command is delete-free and
				// its routine trigger-driven replaces destroy nothing — hence
				// suppressOnReplace: the ⚠ fires only on true removal.
				suppressOnReplace: true,
				destroys:          fmt.Sprintf("removes service %q's descriptor.yaml + secrets.age — the service dies on its next restart (pre-ADR-0042 recorded delete)", s)}
		}
		i.kind = "service"
	case "dbrole":
		if s, ok := cut("-mint"); ok {
			return opInfo{kind: "db role", subject: s, scope: scope,
				destroys: fmt.Sprintf("DROPs Postgres role %q (its objects are reassigned to the database owner; the service loses its DB credential)", s)}
		}
		i.kind = "db role"
	case "db":
		// The per-database <cluster>-db-<database> ensure is matched FIRST: a
		// database legitimately named "install"/"apply"/... would otherwise be
		// eaten by the cluster step-suffix cuts below.
		if cluster, database, ok := strings.Cut(tail, "-db-"); ok {
			return opInfo{kind: "database", subject: database + " @ " + cluster, scope: scope,
				// The -db- command has NO delete script: a delete op here is Pulumi
				// forgetting the resource. Say so — silence is how data-bearing
				// state gets orphaned invisibly.
				destroys: fmt.Sprintf("forgets database %q from state ONLY — the on-host database and its data are NOT deleted; drop it manually if intended", database)}
		}
		// The cluster command chain: <cluster>-install|-mount|-init|-apply.
		for _, step := range []struct{ suffix, kind string }{
			{"-install", "postgres install"}, {"-mount", "postgres volume"},
			{"-init", "postgres initdb"}, {"-apply", "postgres config"},
		} {
			if s, ok := cut(step.suffix); ok {
				return opInfo{kind: step.kind, subject: s, scope: scope}
			}
		}
		i.kind = "db cluster"
	case "dbbackup":
		if s, ok := cut("-credential"); ok {
			return opInfo{kind: "backup credential", subject: s, scope: scope}
		}
		return opInfo{kind: "backup timer", subject: tail, scope: scope,
			destroys: "removes the on-host backup timer (existing backups in R2 are kept)"}
	case "otelcol":
		if s, ok := cut("-install"); ok {
			return opInfo{kind: "otel collector install", subject: s, scope: scope}
		}
		if s, ok := cut("-config"); ok {
			return opInfo{kind: "otel collector config", subject: s, scope: scope}
		}
		i.kind = "otel collector"
	case "mesh":
		if s, ok := cut("-agent"); ok {
			return opInfo{kind: "mesh descriptor", subject: s, scope: scope,
				destroys: "removes the host's mesh-descriptor.yaml"}
		}
		i.kind = "mesh proxy"
	case "vm":
		i.kind, i.destroys = "server", fmt.Sprintf("DESTROYS server %q and its disk", tail)
	case "vol":
		i.kind, i.destroys = "data volume", fmt.Sprintf("DESTROYS data volume %q and everything stored on it (a Postgres cluster's PGDATA lives here)", tail)
	case "fw":
		i.kind, i.destroys = "firewall", "deletes the cloud firewall"
	case "net":
		i.kind, i.destroys = "network", "deletes the private network"
	case "subnet":
		i.kind, i.destroys = "subnet", "deletes the subnet"
	case "record":
		i.kind, i.destroys = "dns record", "deletes the DNS record"
	case "key":
		i.kind = "ssh key"
	}
	return i
}

// diffSuffix renders the engine's changed/replace-causing keys as a compact
// suffix: "  [~create,update]" for an update's changed keys, "  [replaces:
// triggers]" when keys force a replacement. Empty when the engine reported
// nothing.
func diffSuffix(m apitype.StepEventMetadata) string {
	if len(m.Keys) > 0 && (m.Op == apitype.OpCreateReplacement || m.Op == apitype.OpDeleteReplaced || m.Op == apitype.OpReplace) {
		return "  [replaces: " + strings.Join(m.Keys, ",") + "]"
	}
	if len(m.Diffs) > 0 {
		return "  [~" + strings.Join(m.Diffs, ",") + "]"
	}
	return ""
}
