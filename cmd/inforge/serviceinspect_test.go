package main

import (
	"strings"
	"testing"

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
