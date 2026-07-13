package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/wardnet/inforge/internal/service"
)

func targets() []service.DeployTarget {
	return []service.DeployTarget{
		{Service: "ddns", HostDNS: "edge.vm.prd.euc.wardnet.network", Unit: "wardnet-ddns", Scope: "eu-central", SSHUser: "deploy"},
		{Service: "ddns", HostDNS: "edge.vm.prd.use1.wardnet.network", Unit: "wardnet-ddns", Scope: "us-east-1", SSHUser: "deploy"},
	}
}

func TestSelectTargets(t *testing.T) {
	tests := []struct {
		name string
		sel  selector
		want []string // matched HostDNS
	}{
		{"no selector matches every target", selector{}, []string{
			"edge.vm.prd.euc.wardnet.network", "edge.vm.prd.use1.wardnet.network",
		}},
		{"region narrows to one scope", selector{region: "eu-central"}, []string{
			"edge.vm.prd.euc.wardnet.network",
		}},
		{"full host DNS matches exactly", selector{host: "edge.vm.prd.use1.wardnet.network"}, []string{
			"edge.vm.prd.use1.wardnet.network",
		}},
		{"unknown region matches nothing", selector{region: "ap-south-1"}, nil},
		{"unknown host matches nothing", selector{host: "bridge"}, nil},
		{"region and host must BOTH match", selector{region: "eu-central", host: "edge.vm.prd.use1.wardnet.network"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectTargets(targets(), tc.sel)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d targets, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if got[i].HostDNS != w {
					t.Errorf("target %d: got %q, want %q", i, got[i].HostDNS, w)
				}
			}
		})
	}
}

// An operator thinks in compute names ("edge"), not DNS records. A bare host name must
// match the FQDN it is the first label of — but only as a whole label, so "edg" does not
// match "edge…" and quietly act on a host the operator did not name.
func TestSelectTargetsMatchesBareHostNameOnLabelBoundary(t *testing.T) {
	got := selectTargets(targets(), selector{host: "edge", region: "eu-central"})
	if len(got) != 1 || got[0].HostDNS != "edge.vm.prd.euc.wardnet.network" {
		t.Fatalf("bare host name should match the FQDN's first label, got %+v", got)
	}
	if got := selectTargets(targets(), selector{host: "edg"}); len(got) != 0 {
		t.Fatalf("a partial label must NOT match, got %+v", got)
	}
}

func TestParseInspect(t *testing.T) {
	out := strings.Join([]string{
		"state=active",
		"pid=128326",
		"since=Sun 2026-07-12 15:25:26 UTC",
		"restarts=1",
		"instance_id=9f3c1a7e2b5d",
		"exe=/srv/wardnet/ddns/wardnet-ddns",
		"running=aaaa1111",
		"ondisk=aaaa1111",
		"unknown_key=ignored",
		"",
	}, "\n")

	got := parseInspect(out)
	if got.State != "active" || got.PID != "128326" || got.Restarts != "1" {
		t.Errorf("systemd fields wrong: %+v", got)
	}
	if got.InstanceID != "9f3c1a7e2b5d" {
		t.Errorf("instance id: got %q", got.InstanceID)
	}
	if got.Since != "Sun 2026-07-12 15:25:26 UTC" {
		t.Errorf("since must survive its embedded spaces, got %q", got.Since)
	}
	if got.Exe != "/srv/wardnet/ddns/wardnet-ddns" {
		t.Errorf("exe: got %q", got.Exe)
	}
}

func TestIntegrity(t *testing.T) {
	tests := []struct {
		name string
		in   instance
		want string
	}{
		{
			// The failure this column exists for: a new binary is on disk but the process
			// never restarted onto it, so it runs yesterday's code while every dashboard
			// reports the new SHA.
			name: "executing image differs from the one on disk",
			in:   instance{State: "active", Running: "aaaa", OnDisk: "bbbb"},
			want: "STALE",
		},
		{
			name: "executing image matches disk",
			in:   instance{State: "active", Running: "aaaa", OnDisk: "aaaa"},
			want: "ok",
		},
		{
			// A dead process has no /proc/<pid>/exe, so there is nothing to compare and
			// claiming "ok" would be a lie.
			name: "not running",
			in:   instance{State: "failed"},
			want: "-",
		},
		{
			name: "running but checksums unavailable",
			in:   instance{State: "active"},
			want: "unknown",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.integrity(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequireInstanceIsAPreconditionNotAFilter(t *testing.T) {
	live := []instance{
		{Target: targets()[0], InstanceID: "aaaa1111"},
		{Target: targets()[1], InstanceID: "bbbb2222"},
	}

	t.Run("unset acts on every target", func(t *testing.T) {
		got, err := requireInstance(live, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d targets, want 2", len(got))
		}
	})

	t.Run("a matching id narrows to its host", func(t *testing.T) {
		got, err := requireInstance(live, "bbbb2222")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 || got[0].HostDNS != targets()[1].HostDNS {
			t.Fatalf("got %+v", got)
		}
	})

	// The race this closes: you list, you read, you think, you fire — and in between the
	// service crash-looped and came back as a different incarnation. A filter would
	// silently act on nothing (or, with --host, on the WRONG process). Refuse instead,
	// and say what changed — the refusal is itself the diagnosis.
	t.Run("a stale id is refused, and names what is live now", func(t *testing.T) {
		_, err := requireInstance(live, "deadbeef")
		if err == nil {
			t.Fatal("a stale instance id must be refused, not silently matched to nothing")
		}
		if !strings.Contains(err.Error(), "the service restarted") {
			t.Errorf("the error must explain WHY the id is gone, got: %v", err)
		}
		if !strings.Contains(err.Error(), "aaaa1111") || !strings.Contains(err.Error(), "bbbb2222") {
			t.Errorf("the error must name the live instances, got: %v", err)
		}
	})

	t.Run("a stale id with nothing running says so", func(t *testing.T) {
		_, err := requireInstance([]instance{{Target: targets()[0]}}, "deadbeef")
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("want a 'not running' error, got: %v", err)
		}
	})
}

// The probe must survive a service that is DOWN: no MainPID means no /proc entry, and the
// command still has to answer "is it even running".
func TestInspectScriptGuardsAgainstNoMainPID(t *testing.T) {
	got := inspectScript("wardnet-ddns")
	if !strings.Contains(got, `[ "$pid" != "0" ]`) {
		t.Error("the /proc probes must be guarded on a non-zero MainPID")
	}
	if !strings.Contains(got, "sha256sum /proc/$pid/exe") {
		t.Error("the running-image checksum must read /proc/<pid>/exe (the executing inode), not the path")
	}
	if !strings.Contains(got, "(deleted)") {
		t.Error("a replaced binary leaves ' (deleted)' on the exe symlink; it must be stripped")
	}
}

func TestJournalScript(t *testing.T) {
	got := journalScript("wardnet-ddns", 50, "-30min", false)
	for _, want := range []string{"journalctl", "-u 'wardnet-ddns'", "-n 50", "--since '-30min'", "--no-pager"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in: %s", want, got)
		}
	}
	if strings.Contains(got, " -f") {
		t.Error("follow must not be set when not requested")
	}

	// --since is free text from the operator — the one input here that is not a fixed
	// template, so it must be quoted.
	quoted := journalScript("wardnet-ddns", 10, "yesterday; rm -rf /", false)
	if !strings.Contains(quoted, `--since 'yesterday; rm -rf /'`) {
		t.Errorf("--since must be shell-quoted, got: %s", quoted)
	}
}

func TestMeshCheckScriptReadsTheEgressURLFromTheDescriptor(t *testing.T) {
	got := meshCheckScript("ddns", "tenants", "/v1/networks")

	// Recomputing the positional egress-port assignment here would be a second source of
	// truth for something meshpaths.EgressPort already owns, and could silently drift
	// from what was actually deployed. Read what the host was given.
	if !strings.Contains(got, "/etc/wardnet/services/ddns/descriptor.yaml") {
		t.Error("the egress URL must be read from the service's on-host descriptor")
	}
	if !strings.Contains(got, "X-Mesh-Target: tenants") {
		t.Error("the peer must be named in X-Mesh-Target")
	}
	if !strings.Contains(got, "/v1/networks") {
		t.Error("the requested path must reach the call")
	}
	if !strings.Contains(got, "%{http_code}") {
		t.Error("the probe must report the status code and nothing else")
	}
}

// The status readings below are not invented — each was observed against the live prd
// mesh (ddns -> tenants) while building this command.
func TestMeshVerdict(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
		ok     bool
	}{
		{
			// The single most counter-intuitive reading in the command, and the one most
			// likely to be "fixed" into a bug later. The callee's mesh proxy serves only
			// its declared paths and answers anything else with a JSON 404 — but that 404
			// comes from INSIDE the allow-guard, so receiving it proves we authenticated,
			// were admitted, and were routed. Observed: `/` -> tenants -> 404.
			name: "404 from the catch-all is a PASS", status: "404", want: "reachable", ok: true,
		},
		{name: "2xx on a declared path", status: "200", want: "reachable", ok: true},
		{
			// Observed: `/v1/networks` -> tenants -> 400 (the app rejected the missing
			// query params). The app answering AT ALL proves the mesh carried the request.
			name: "4xx from the app still proves the mesh carried it", status: "400", want: "reachable", ok: true,
		},
		{
			name: "403 is the allow-guard refusing us", status: "403",
			want: "FORBIDDEN (not in callee's allowed_services, or its allow-map is stale)", ok: false,
		},
		{
			name: "000 never completed", status: "000",
			want: "UNREACHABLE (connectivity/TLS)", ok: false,
		},
		{
			// Observed: a bogus --target returns 502 (the egress cannot route an unknown
			// X-Mesh-Target) — and so does a callee whose app is down behind a healthy
			// mesh proxy. Claiming to know which would be a lie, so we say so and FAIL,
			// rather than reporting a reachable mesh the operator has not proven.
			name: "5xx is ambiguous and must not pass", status: "502",
			want: "AMBIGUOUS 5xx (unknown --target, or the callee's app is down)", ok: false,
		},
		{name: "no status", status: "", want: "no status", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := meshVerdict(tc.status)
			if got != tc.want {
				t.Errorf("status %q: got %q, want %q", tc.status, got, tc.want)
			}
			if ok != tc.ok {
				t.Errorf("status %q: pass=%v, want %v", tc.status, ok, tc.ok)
			}
		})
	}
}

func TestStatusOf(t *testing.T) {
	out := "egress=http://127.0.0.1:9500\nstatus=403\n"
	if got := statusOf(out); got != "403" {
		t.Errorf("got %q, want 403", got)
	}
	if got := statusOf("egress=http://127.0.0.1:9500\n"); got != "" {
		t.Errorf("a missing status must be empty, got %q", got)
	}
}

// ── the flows, exercised through the injected runner ──────────────────────────────
//
// Everything below used to be untestable: it was welded to a real `ssh` exec. The
// sshRunner seam is what makes the work — not just the helpers around it — verifiable.

// fakeSSH replays canned output per remote account, and errors for any account listed in
// `fail` (an unreachable host).
type fakeSSH struct {
	out  map[string]string // account -> stdout
	fail map[string]bool   // account -> connection error
	ran  []string          // the remote commands actually issued, in order
}

func (f *fakeSSH) run(_ context.Context, _, account, remoteCmd string, w io.Writer) error {
	f.ran = append(f.ran, remoteCmd)
	if f.fail[account] {
		return errors.New("ssh: connect to host: connection refused")
	}
	_, err := io.WriteString(w, f.out[account])
	return err
}

func probeOutput(state, id, running, ondisk string) string {
	return fmt.Sprintf("state=%s\npid=42\nsince=Mon 2026-07-13 11:00:00 UTC\nrestarts=0\ninstance_id=%s\nexe=/srv/wardnet/ddns/wardnet-ddns\nrunning=%s\nondisk=%s\n",
		state, id, running, ondisk)
}

func TestCollectInstancesWith(t *testing.T) {
	tt := targets()
	euc, use1 := sshAccount(tt[0]), sshAccount(tt[1])

	f := &fakeSSH{
		out:  map[string]string{euc: probeOutput("active", "aaaa1111", "cafe", "cafe")},
		fail: map[string]bool{use1: true},
	}
	got := collectInstancesWith(context.Background(), tt, "key", f.run)

	if len(got) != 2 {
		t.Fatalf("every target must yield a row, got %d", len(got))
	}
	if got[0].InstanceID != "aaaa1111" || got[0].State != "active" {
		t.Errorf("reachable host parsed wrong: %+v", got[0])
	}
	if got[0].Target.HostDNS != tt[0].HostDNS {
		t.Error("the target must be stitched back onto its instance")
	}

	// The whole point of not aborting the sweep: "one of your hosts is unreachable" is
	// the finding you called the command to get. Bailing on the first bad host would
	// have hidden the healthy one entirely.
	if got[1].State != "unreachable" {
		t.Errorf("an unreachable host must be reported, not fatal: %+v", got[1])
	}
	if got[1].integrity() != "-" {
		t.Errorf("an unreachable host has no integrity verdict, got %q", got[1].integrity())
	}
}

func TestRenderInstances(t *testing.T) {
	tt := targets()
	var buf bytes.Buffer
	err := renderInstances(&buf, []instance{
		{Target: tt[0], State: "active", InstanceID: "aaaa1111bbbb2222", Since: "Mon", Restarts: "0", Running: "cafe", OnDisk: "cafe"},
		{Target: tt[1], State: "active", InstanceID: "cccc3333", Since: "Mon", Restarts: "2", Running: "cafe", OnDisk: "f00d"},
	}, "prd", "ddns")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"HOST", "INTEGRITY", tt[0].HostDNS, "eu-central", "ok", "STALE"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// STALE scrolling past unread is the failure mode the call-out exists to prevent, so
	// the table alone is not enough — it must be named, counted, and told what to do.
	if !strings.Contains(out, "1 instance(s) STALE") {
		t.Errorf("a STALE instance must be called out below the table:\n%s", out)
	}
	if !strings.Contains(out, "inforge service restart prd ddns") {
		t.Errorf("the STALE call-out must name the fix:\n%s", out)
	}
}

func TestRenderInstancesStaysQuietWhenNothingIsStale(t *testing.T) {
	var buf bytes.Buffer
	if err := renderInstances(&buf, []instance{
		{Target: targets()[0], State: "active", Running: "cafe", OnDisk: "cafe"},
	}, "prd", "ddns"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "STALE") {
		t.Errorf("a healthy fleet must not mention STALE at all:\n%s", buf.String())
	}
}

// An empty field must render as "-" rather than collapsing the column — a service that is
// down has no instance id, no uptime and no running image.
func TestRenderInstancesDashesEmptyFields(t *testing.T) {
	var buf bytes.Buffer
	if err := renderInstances(&buf, []instance{{Target: targets()[0], State: "inactive"}}, "prd", "ddns"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "-") {
		t.Errorf("empty fields must dash:\n%s", buf.String())
	}
}

func TestMeshCheck(t *testing.T) {
	tt := targets()
	euc, use1 := sshAccount(tt[0]), sshAccount(tt[1])

	f := &fakeSSH{out: map[string]string{
		// 404 from the callee's catch-all: allowed and routed. A PASS.
		euc: "egress=http://127.0.0.1:9500\nstatus=404\n",
		// 403: the allow-guard refusing us — the exact incident this command exists for,
		// and here it happens in ONE region only, which a bail-on-first-failure sweep
		// would have reported as a total outage.
		use1: "egress=http://127.0.0.1:9500\nstatus=403\n",
	}}

	got := meshCheck(context.Background(), tt, "key", "ddns", "tenants", "/", f.run)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if !got[0].OK || got[0].Status != "404" {
		t.Errorf("404 must pass: %+v", got[0])
	}
	if got[0].Egress != "http://127.0.0.1:9500" {
		t.Errorf("the egress url must be reported: %+v", got[0])
	}
	if got[1].OK {
		t.Errorf("403 must fail: %+v", got[1])
	}
	// The probe must actually be the mesh call, not something else.
	if !strings.Contains(f.ran[0], "X-Mesh-Target: tenants") {
		t.Errorf("the probe did not address the peer: %s", f.ran[0])
	}
}

func TestMeshCheckReportsAnUnreachableHostInsteadOfAborting(t *testing.T) {
	tt := targets()
	f := &fakeSSH{fail: map[string]bool{sshAccount(tt[0]): true, sshAccount(tt[1]): true}}
	got := meshCheck(context.Background(), tt, "key", "ddns", "tenants", "/", f.run)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	for _, r := range got {
		if r.OK || !strings.Contains(r.Verdict, "probe failed") {
			t.Errorf("an unreachable host must be a failed result, not a fatal error: %+v", r)
		}
	}
}

func TestRenderMeshCheckFailsWhenAnyHostCannotReachThePeer(t *testing.T) {
	tt := targets()
	var buf bytes.Buffer
	err := renderMeshCheck(&buf, []meshResult{
		{Target: tt[0], Egress: "http://127.0.0.1:9500", Status: "404", Verdict: "reachable", OK: true},
		{Target: tt[1], Egress: "http://127.0.0.1:9500", Status: "403", Verdict: "FORBIDDEN (…)", OK: false},
	}, "ddns", "tenants", "/")

	// A mesh that works from one region and not another is exactly the shape of the
	// incident this command exists for. Reporting overall success because SOME host got
	// through would bury it.
	if err == nil {
		t.Fatal("one unreachable host must fail the command")
	}
	out := buf.String()
	if !strings.Contains(out, "404") || !strings.Contains(out, "403") {
		t.Errorf("both hosts' results must still be printed:\n%s", out)
	}
}

func TestRenderMeshCheckSucceedsWhenEveryHostReachesThePeer(t *testing.T) {
	var buf bytes.Buffer
	if err := renderMeshCheck(&buf, []meshResult{
		{Target: targets()[0], Status: "404", Verdict: "reachable", OK: true},
	}, "ddns", "tenants", "/"); err != nil {
		t.Fatalf("an all-reachable mesh must pass, got: %v", err)
	}
	if !strings.Contains(buf.String(), "ddns -> tenants") {
		t.Errorf("the header must name the call:\n%s", buf.String())
	}
}

func TestStreamLogs(t *testing.T) {
	tt := targets()
	f := &fakeSSH{out: map[string]string{
		sshAccount(tt[0]): "euc log line\n",
		sshAccount(tt[1]): "use1 log line\n",
	}}

	var buf bytes.Buffer
	if err := streamLogs(context.Background(), &buf, tt, "key", 50, "-30min", false, f.run); err != nil {
		t.Fatalf("streamLogs: %v", err)
	}
	out := buf.String()
	// Two hosts' journals merged with no headers would be unreadable.
	for _, want := range []string{tt[0].HostDNS, "euc log line", tt[1].HostDNS, "use1 log line"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(f.ran[0], "--since '-30min'") || !strings.Contains(f.ran[0], "-n 50") {
		t.Errorf("the flags must reach journalctl: %s", f.ran[0])
	}
}

func TestStreamLogsSingleTargetIsUnheaded(t *testing.T) {
	tt := targets()[:1]
	f := &fakeSSH{out: map[string]string{sshAccount(tt[0]): "just the log\n"}}
	var buf bytes.Buffer
	if err := streamLogs(context.Background(), &buf, tt, "key", 10, "", false, f.run); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "===") {
		t.Errorf("a single host needs no header:\n%s", buf.String())
	}
}

func TestSSHAccount(t *testing.T) {
	if got := sshAccount(targets()[0]); got != "deploy@edge.vm.prd.euc.wardnet.network" {
		t.Errorf("got %q", got)
	}
	// BuildDeployDescriptor always sets SSHUser, but a descriptor written before it did
	// must not produce "@host".
	bare := service.DeployTarget{HostDNS: "h", SSHUser: ""}
	if got := sshAccount(bare); got != "deploy@h" {
		t.Errorf("empty SSHUser must fall back to deploy, got %q", got)
	}
}

// The commands are wired by hand, so a renamed flag or a changed arg count is a silent
// break the compiler cannot catch.
func TestCommandsAreWired(t *testing.T) {
	cfg := "inforge.yaml"
	root := newServiceCmd(&cfg)

	want := map[string][]string{
		"instances":  {"region", "host", "stack-config", "ssh-key"},
		"logs":       {"region", "host", "lines", "since", "follow"},
		"status":     {"region", "host"},
		"mesh-check": {"region", "host", "target", "path"},
		"restart":    {"region", "host", "instance"}, // --instance: the precondition
	}
	for verb, flags := range want {
		var found *cobra.Command
		for _, c := range root.Commands() {
			if strings.HasPrefix(c.Use, verb) {
				found = c
				break
			}
		}
		if found == nil {
			t.Errorf("`service %s` is not registered", verb)
			continue
		}
		for _, f := range flags {
			if found.Flags().Lookup(f) == nil {
				t.Errorf("`service %s` is missing --%s", verb, f)
			}
		}
	}

	// --instance is meaningless where nothing acts on a live process, and offering it
	// would imply a precondition that is never checked.
	for _, c := range root.Commands() {
		if strings.HasPrefix(c.Use, "instances") && c.Flags().Lookup("instance") != nil {
			t.Error("`service instances` must not offer --instance: it lists them, it does not target one")
		}
	}
}

func TestStreamStatus(t *testing.T) {
	tt := targets()
	f := &fakeSSH{out: map[string]string{
		sshAccount(tt[0]): "● wardnet-ddns.service - active (running)\n",
		sshAccount(tt[1]): "● wardnet-ddns.service - inactive (dead)\n",
	}}
	var buf bytes.Buffer
	if err := streamStatus(context.Background(), &buf, tt, "key", f.run); err != nil {
		t.Fatalf("streamStatus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{tt[0].HostDNS, "active (running)", tt[1].HostDNS, "inactive (dead)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// systemctl exits non-zero for an inactive unit. Without `|| true` the sweep would abort
// on the first DOWN service — the very thing you ran the command to find.
func TestStatusScriptToleratesADeadUnit(t *testing.T) {
	got := statusScript("wardnet-ddns")
	if !strings.Contains(got, "|| true") {
		t.Errorf("a dead unit must not abort the sweep: %s", got)
	}
	if !strings.Contains(got, "'wardnet-ddns'") {
		t.Errorf("the unit must be quoted: %s", got)
	}
}
