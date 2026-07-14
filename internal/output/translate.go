package output

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
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

// namedTypes are the <type> tokens of the resource-name grammar
// (`wardnet-<env>[-<slug>]-<type>-<rest>`). The token AFTER the env is a region
// slug only when it is not one of these — naming.GlobalResource omits the slug.
var namedTypes = map[string]bool{
	"vm": true, "fw": true, "net": true, "subnet": true, "db": true,
	"project": true, "secrets": true, "workspace": true, "record": true,
	"ingress": true, "svc": true, "identity": true, "app": true, "key": true,
	"dbrole": true, "dbbackup": true, "mesh": true, "otelcol": true,
}

// translate parses a Pulumi resource name (plus its type token) into the human
// opInfo. Unrecognized names fall back to the short type + raw name, so the
// output never hides a resource it cannot explain.
func translate(typ, name string) opInfo {
	info, ok := parseWardnetName(name)
	if !ok {
		return opInfo{kind: shortType(typ), subject: name}
	}
	return info
}

// parseWardnetName decodes `wardnet-<env>[-<slug>]-<type>-<rest>` and the
// well-known command suffixes appended to those names.
func parseWardnetName(name string) (opInfo, bool) {
	parts := strings.Split(name, "-")
	if len(parts) < 4 || parts[0] != "wardnet" {
		return opInfo{}, false
	}
	// parts[1] is the env. parts[2] is either the region slug or (for an
	// env-global name) already the type token.
	scope, rest := "", parts[2:]
	if !namedTypes[rest[0]] {
		scope, rest = rest[0], rest[1:]
		if len(rest) < 2 || !namedTypes[rest[0]] {
			return opInfo{}, false
		}
	}
	typ, tail := rest[0], strings.Join(rest[1:], "-")
	return classify(typ, tail, scope), true
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
				destroys: fmt.Sprintf("removes service %q's descriptor.yaml + secrets.age — the service dies on its next restart (pre-ADR-0042 recorded delete)", s)}
		}
		i.kind = "service"
	case "dbrole":
		if s, ok := cut("-mint"); ok {
			return opInfo{kind: "db role", subject: s, scope: scope,
				destroys: fmt.Sprintf("DROPs Postgres role %q (its objects are reassigned to the database owner; the service loses its DB credential)", s)}
		}
		i.kind = "db role"
	case "db":
		// The cluster command chain: <cluster>-install|-mount|-init|-apply and the
		// per-database <cluster>-db-<database> ensure.
		for _, step := range []struct{ suffix, kind string }{
			{"-install", "postgres install"}, {"-mount", "postgres volume"},
			{"-init", "postgres initdb"}, {"-apply", "postgres config"},
		} {
			if s, ok := cut(step.suffix); ok {
				return opInfo{kind: step.kind, subject: s, scope: scope}
			}
		}
		if cluster, database, ok := strings.Cut(tail, "-db-"); ok {
			return opInfo{kind: "database", subject: database + " @ " + cluster, scope: scope,
				// The -db- command has NO delete script: a delete op here is Pulumi
				// forgetting the resource. Say so — silence is how data-bearing
				// state gets orphaned invisibly.
				destroys: fmt.Sprintf("forgets database %q from state ONLY — the on-host database and its data are NOT deleted; drop it manually if intended", database)}
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
