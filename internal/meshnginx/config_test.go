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
			},
			{
				Name: "ddns", SNI: "ddns.us-east-1.mesh", MeshPort: 9090,
				LeafCertPath: "/run/wardnet/mesh/ddns/leaf.crt", LeafKeyPath: "/run/wardnet/mesh/ddns/leaf.key",
				AllowedCallers: []string{"us-east-1/tenants"},
			},
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
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
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
