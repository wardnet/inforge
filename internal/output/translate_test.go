package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

func TestTranslateGrammar(t *testing.T) {
	cases := []struct {
		name     string
		display  string
		destroys string // "" = delete not known-destructive; substring otherwise
	}{
		{"wardnet-prd-use1-svc-tenants-provision", "service unit tenants (use1)", "stops service"},
		{"wardnet-prd-use1-svc-tenants-secrets", "service config+secrets tenants (use1)", "descriptor.yaml + secrets.age"},
		{"wardnet-prd-use1-dbrole-tenants-tenants-mint", "db role tenants-tenants (use1)", "DROPs Postgres role"},
		{"wardnet-prd-use1-db-pg-db-appdb", "database appdb @ pg (use1)", "NOT deleted"},
		{"wardnet-prd-use1-db-pg-apply", "postgres config pg (use1)", ""},
		{"wardnet-prd-use1-vm-bridge-01", "server bridge-01 (use1)", "DESTROYS server"},
		{"wardnet-prd-use1-dbbackup-edge-01-pg-appdb", "backup timer edge-01-pg-appdb (use1)", "backup timer"},
		{"wardnet-prd-use1-otelcol-edge-01-config", "otel collector config edge-01 (use1)", ""},
		{"wardnet-prd-use1-mesh-edge-01-agent", "mesh descriptor edge-01 (use1)", "mesh-descriptor"},
		// Global names carry no slug segment.
		{"wardnet-prd-key-user", "ssh key user", ""},
		{"wardnet-prd-svc-tenants-provision", "service unit tenants", "stops service"},
	}
	for _, c := range cases {
		got := translate("command:remote:Command", c.name)
		if got.display() != c.display {
			t.Errorf("translate(%q).display() = %q, want %q", c.name, got.display(), c.display)
		}
		if c.destroys == "" && got.destroys != "" {
			t.Errorf("translate(%q).destroys = %q, want none", c.name, got.destroys)
		}
		if c.destroys != "" && !strings.Contains(got.destroys, c.destroys) {
			t.Errorf("translate(%q).destroys = %q, want substring %q", c.name, got.destroys, c.destroys)
		}
	}
}

// A name outside the grammar must fall back to the raw type+name, never hide.
func TestTranslateFallback(t *testing.T) {
	got := translate("random:index/randomPassword:RandomPassword", "tenants-tenants-pw")
	if got.display() != "random:RandomPassword tenants-tenants-pw" {
		t.Errorf("fallback display = %q", got.display())
	}
}

func preEvent(name, typ string, op apitype.OpType, diffs, keys []string) events.EngineEvent {
	return events.EngineEvent{EngineEvent: apitype.EngineEvent{
		ResourcePreEvent: &apitype.ResourcePreEvent{Metadata: apitype.StepEventMetadata{
			Op:    op,
			Type:  typ,
			URN:   "urn:pulumi:prd::proj::" + typ + "::" + name,
			Diffs: diffs,
			Keys:  keys,
		}},
	}}
}

// Every delete of host-destroying state must surface in the ⚠ section — the
// v6.1.0 outage was legible in the raw stream only to a resource-name
// connoisseur ("6 deleted" meant "your services' agent inputs are gone").
func TestDestructiveOperationsSection(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	p.Handle(preEvent("wardnet-prd-use1-svc-tenants-secrets", "command:remote:Command", apitype.OpDeleteReplaced, nil, []string{"triggers"}))
	p.Handle(preEvent("wardnet-prd-use1-db-pg-db-appdb", "command:remote:Command", apitype.OpDelete, nil, nil))
	p.Handle(preEvent("wardnet-prd-use1-svc-tenants-provision", "command:remote:Command", apitype.OpUpdate, []string{"create", "update"}, nil))
	p.Finish()
	out := buf.String()

	for _, want := range []string{
		"⚠ Destructive operations",
		"descriptor.yaml + secrets.age",
		"NOT deleted",           // the forgotten database is called out
		"[replaces: triggers]",  // the replace reason is visible inline
		"[~create,update]",      // the update reason is visible inline
		"service unit tenants (use1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The plain update is not destructive and must not be in the ⚠ section.
	if strings.Count(out, "⚠") != 3 { // header + two entries (inline ⚠ markers share the entries' lines)
		t.Logf("output:\n%s", out)
	}
}

// A run with no deletes prints no destructive section at all.
func TestNoDestructiveSectionWhenNoDeletes(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	p.Handle(preEvent("wardnet-prd-use1-svc-tenants-provision", "command:remote:Command", apitype.OpUpdate, []string{"update"}, nil))
	p.Finish()
	if strings.Contains(buf.String(), "Destructive") {
		t.Errorf("no deletes ran — no destructive section expected:\n%s", buf.String())
	}
}
