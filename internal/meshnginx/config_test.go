package meshnginx

import (
	"strings"
	"testing"
)

func sampleConfig() Config {
	return Config{
		ListenAddr:      "10.0.0.5",
		TrustBundlePath: "/run/wardnet/mesh/bundle.crt",
		Local: []LocalService{
			{
				Name: "tenants", SNI: "tenants.us-east-1.mesh", MeshPort: 8080,
				LeafCertPath: "/run/wardnet/mesh/tenants/leaf.crt", LeafKeyPath: "/run/wardnet/mesh/tenants/leaf.key",
				AllowedCallers: []string{"us-east-1/ddns", "us-east-1/gateway"},
				Paths:          []string{"/v*/account/**", "/internal/reconcile"},
			},
			{
				Name: "ddns", SNI: "ddns.us-east-1.mesh", MeshPort: 9090,
				LeafCertPath: "/run/wardnet/mesh/ddns/leaf.crt", LeafKeyPath: "/run/wardnet/mesh/ddns/leaf.key",
				AllowedCallers: []string{"us-east-1/tenants"},
				Paths:          []string{"/dns/**"},
			},
		},
		Egress: []EgressCaller{
			{Name: "tenants", EgressPort: 9500, LeafCertPath: "/run/wardnet/mesh/tenants/leaf.crt", LeafKeyPath: "/run/wardnet/mesh/tenants/leaf.key"},
			{Name: "ddns", EgressPort: 9501, LeafCertPath: "/run/wardnet/mesh/ddns/leaf.crt", LeafKeyPath: "/run/wardnet/mesh/ddns/leaf.key"},
		},
		Targets: []Target{
			{Name: "tenants", Addr: "10.0.0.5:8443", SNI: "tenants.us-east-1.mesh"},
			{Name: "ddns", Addr: "10.0.0.5:8443", SNI: "ddns.us-east-1.mesh"},
			{Name: "billing", Addr: "203.0.113.9:8443", SNI: "billing.global.mesh"},
		},
	}
}

func TestRenderIngress(t *testing.T) {
	out, err := Render(sampleConfig())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"map $http_upgrade $connection_upgrade",
		"map $ssl_client_s_dn $mesh_caller",
		`"~^CN=(?<cn>.+)$" $cn;`,
		"map $mesh_caller $mesh_allow_tenants",
		"us-east-1/ddns 1;",
		"us-east-1/gateway 1;",
		"server_name tenants.us-east-1.mesh;",
		"listen 10.0.0.5:8443 ssl;",
		"ssl_verify_client on;",
		"ssl_client_certificate /run/wardnet/mesh/bundle.crt;",
		"if ($mesh_allow_tenants = 0)",
		"return 403;",
		"proxy_set_header X-Service-Identity $mesh_caller;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_pass http://127.0.0.1:8080;",
		"proxy_pass http://127.0.0.1:9090;",
		"pid /run/wardnet-mesh-nginx.pid;",
		// egress plane
		"map $http_x_mesh_target $mesh_addr",
		"map $http_x_mesh_target $mesh_sni",
		"billing 203.0.113.9:8443;",
		"tenants 10.0.0.5:8443;",
		"billing.global.mesh;",
		"listen 127.0.0.1:9500;",
		"listen 127.0.0.1:9501;",
		`if ($mesh_addr = "")`,
		"return 502;",
		"proxy_ssl_certificate /run/wardnet/mesh/tenants/leaf.crt;",
		"proxy_ssl_name $mesh_sni;",
		"proxy_ssl_verify on;",
		"proxy_pass https://$mesh_addr;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderIngressPathAllowlist: a callee with declared path globs gets one
// regex location per compiled glob (each carrying the allow-list guard) plus a
// JSON 404 catch-all — an undeclared path is unreachable even for an allowed
// peer (ADR-0034).
func TestRenderIngressPathAllowlist(t *testing.T) {
	c := sampleConfig()
	c.Local[0].Paths = []string{"/v*/account/**", "/internal/reconcile"}
	out, err := Render(c)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		`location ~ "^/v[^/]+/account(/.*)?$" {`,
		`location ~ "^/internal/reconcile$" {`,
		`default_type application/json;`,
		`return 404 '{"error":"not_found"}';`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
	// The JSON-404 catch-all carries the allow-guard too: a valid-cert peer NOT in
	// allowed_services must get a uniform 403 on every path — 403-on-declared vs
	// 404-on-undeclared would let it enumerate the callee's declared surface.
	if !strings.Contains(out, "location / {\n            if ($mesh_allow_tenants = 0)") {
		t.Errorf("catch-all lost the allow-guard (403 before 404)\n---\n%s", out)
	}
}

// TestRenderIngressNoPathsErrors: a path-less callee fails the render loud — the
// callee surface is allowlist-only, never full-open (ADR-0034).
func TestRenderIngressNoPathsErrors(t *testing.T) {
	c := sampleConfig()
	c.Local[0].Paths = nil
	if _, err := Render(c); err == nil || !strings.Contains(err.Error(), "allowlist-only") {
		t.Errorf("expected allowlist-only error for a path-less callee, got %v", err)
	}
}

// TestRenderIngressBadGlobErrors: a malformed glob fails the render loud.
func TestRenderIngressBadGlobErrors(t *testing.T) {
	c := sampleConfig()
	c.Local[0].Paths = []string{"/a/**/b"}
	if _, err := Render(c); err == nil || !strings.Contains(err.Error(), "invalid path glob") {
		t.Errorf("expected invalid path glob error, got %v", err)
	}
}

func TestRenderDeterministic(t *testing.T) {
	a, err := Render(sampleConfig())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(sampleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("Render is not deterministic")
	}
}

func TestRenderErrors(t *testing.T) {
	if _, err := Render(Config{}); err == nil {
		t.Error("expected error for empty listen address")
	}
	c := sampleConfig()
	c.Local[0].MeshPort = 0
	if _, err := Render(c); err == nil {
		t.Error("expected error for incompletely resolved service")
	}
	c = sampleConfig()
	c.TrustBundlePath = ""
	if _, err := Render(c); err == nil {
		t.Error("expected error for services without a trust bundle")
	}
}
