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
// The -secrets command is the calibrated exception: its trigger-driven REPLACE
// is the routine path for every real secret/descriptor change post-ADR-0042
// (the recorded resource is delete-free), so only a true OpDelete warns —
// a section that cries wolf on every rotation trains operators to ignore it.
func TestDestructiveOperationsSection(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	p.Handle(preEvent("wardnet-prd-use1-svc-tenants-secrets", "command:remote:Command", apitype.OpDeleteReplaced, nil, []string{"triggers"}))
	p.Handle(preEvent("wardnet-prd-use1-svc-ghost-secrets", "command:remote:Command", apitype.OpDelete, nil, nil))
	p.Handle(preEvent("wardnet-prd-use1-db-pg-db-appdb", "command:remote:Command", apitype.OpDelete, nil, nil))
	p.Handle(preEvent("wardnet-prd-use1-vol-pg", "hcloud:index/volume:Volume", apitype.OpDelete, nil, nil))
	p.Handle(preEvent("wardnet-prd-use1-svc-tenants-provision", "command:remote:Command", apitype.OpUpdate, []string{"create", "update"}, nil))
	p.Finish()
	out := buf.String()

	for _, want := range []string{
		"⚠ Destructive operations",
		`service "ghost"`,      // TRUE removal of a service's secrets warns
		"NOT deleted",          // the forgotten database is called out
		"DESTROYS data volume", // the PGDATA volume delete is the loudest line
		"[replaces: triggers]", // the replace reason is visible inline
		"[~create,update]",     // the update reason is visible inline
		"service unit tenants (use1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	section := out[strings.Index(out, "⚠ Destructive operations"):]
	if strings.Contains(section, "tenants") {
		t.Errorf("the routine trigger-driven -secrets replace of tenants must NOT reach the ⚠ section:\n%s", out)
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

func TestOpVerbAndLabels(t *testing.T) {
	cases := map[apitype.OpType][3]string{ // verb, preview label, applied label
		apitype.OpCreate:            {"create", "to create", "created"},
		apitype.OpUpdate:            {"update", "to update", "updated"},
		apitype.OpDelete:            {"delete", "to delete", "deleted"},
		apitype.OpCreateReplacement: {"replace", "to replace", "replaced"},
		apitype.OpDeleteReplaced:    {"delete (for replace)", "to delete (replaced)", "deleted (replaced)"},
	}
	for op, want := range cases {
		if got := opVerb(op); got != want[0] {
			t.Errorf("opVerb(%s) = %q, want %q", op, got, want[0])
		}
		if got := opLabel(op, true); got != want[1] {
			t.Errorf("opLabel(%s, preview) = %q, want %q", op, got, want[1])
		}
		if got := opLabel(op, false); got != want[2] {
			t.Errorf("opLabel(%s, applied) = %q, want %q", op, got, want[2])
		}
	}
	if got := opVerb(apitype.OpImport); got != string(apitype.OpImport) {
		t.Errorf("unknown ops must pass through, got %q", got)
	}
}

// Destructive() exposes the same entries the terminal section shows, for the
// markdown report.
func TestPrinterDestructiveAccessor(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	p.Handle(preEvent("wardnet-prd-use1-vm-bridge-01", "hcloud:index/server:Server", apitype.OpDelete, nil, nil))
	p.Finish()
	if len(p.Destructive()) != 1 {
		t.Fatalf("Destructive() = %v, want one entry", p.Destructive())
	}
	if !strings.Contains(p.Destructive()[0], "DESTROYS server") {
		t.Errorf("entry = %q", p.Destructive()[0])
	}
}

// The pulumi-command remote provider warns about PULUMI_COMMAND_* SSH env vars
// on EVERY command of EVERY deploy — non-actionable noise that buried the real
// lines. It is suppressed; a real warning still prints.
func TestKnownNoiseWarningsAreSuppressed(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)
	diag := func(sev, msg string) events.EngineEvent {
		return events.EngineEvent{EngineEvent: apitype.EngineEvent{
			DiagnosticEvent: &apitype.DiagnosticEvent{Severity: sev, Message: msg},
		}}
	}
	p.Handle(diag("warning", "Unable to set 'PULUMI_COMMAND_STDERR'. This only works if your SSH server is configured to accept these variables via AcceptEnv."))
	p.Handle(diag("warning", "Unable to set 'PULUMI_COMMAND_STDOUT'. This only works if your SSH server is configured to accept these variables via AcceptEnv."))
	p.Handle(diag("warning", "a REAL warning about something else"))
	p.Finish()
	out := buf.String()
	if strings.Contains(out, "PULUMI_COMMAND_") {
		t.Errorf("the AcceptEnv provider noise must be suppressed:\n%s", out)
	}
	if !strings.Contains(out, "a REAL warning about something else") {
		t.Errorf("real warnings must still print:\n%s", out)
	}
}
