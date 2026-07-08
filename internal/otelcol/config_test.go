package otelcol

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func fullAttrs() Attributes {
	return Attributes{
		HostID:           "wardnet-prd-use1-vm-bridge-01",
		CloudProvider:    "hetzner",
		CloudRegion:      "us-east",
		AvailabilityZone: "ash",
		MachineType:      "cx23",
		Environment:      "prd",
		RegionSlug:       "use1",
	}
}

func TestRenderParsesAndCarriesAttributes(t *testing.T) {
	out, err := Render("https://otlp.example/v1", fullAttrs())
	require.NoError(t, err)

	// It must be valid YAML.
	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(out), &cfg))

	// Endpoint and the file-provider auth header (secret never inlined).
	assert.Contains(t, out, "endpoint: https://otlp.example/v1")
	assert.Contains(t, out, "Authorization: Basic ${file:"+CredentialPath+"}")
	assert.NotContains(t, out, "instanceID")

	// Every resource attribute is present, including the shared ADR-0030 set.
	for _, want := range []string{
		"service.name", ServiceName,
		"service.instance.id", "wardnet-prd-use1-vm-bridge-01",
		"host.id", "wardnet-prd-use1-vm-bridge-01",
		"host.type", "cx23",
		"cloud.provider", "hetzner",
		"cloud.region", "us-east",
		"cloud.availability_zone", "ash",
		"deployment.environment.name", "prd",
		"region", "use1",
	} {
		assert.Contains(t, out, want)
	}

	// The hostmetrics receiver is wired with the host scrapers and no `process`.
	assert.Contains(t, out, "hostmetrics")
	assert.NotContains(t, out, "process:")
}

func TestRenderOmitsEmptyAttributes(t *testing.T) {
	// Only the required host id is set; the provider supplied nothing else.
	out, err := Render("https://otlp.example/v1", Attributes{HostID: "node-1"})
	require.NoError(t, err)

	assert.Contains(t, out, "node-1")
	for _, absent := range []string{"cloud.provider", "cloud.region", "cloud.availability_zone", "host.type"} {
		assert.NotContains(t, out, absent, "an empty attribute must be omitted")
	}
	// service.name and service.instance.id are always stamped.
	assert.Contains(t, out, ServiceName)
	assert.Contains(t, out, "service.instance.id")
}

func TestRenderIsDeterministic(t *testing.T) {
	a, err := Render("https://otlp.example/v1", fullAttrs())
	require.NoError(t, err)
	b, err := Render("https://otlp.example/v1", fullAttrs())
	require.NoError(t, err)
	assert.Equal(t, a, b, "render must be byte-stable for a stable resource graph")
}

func TestRenderRequiresEndpointAndHostID(t *testing.T) {
	_, err := Render("", fullAttrs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint")

	_, err = Render("https://otlp.example/v1", Attributes{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host id")
}

func TestInstallScriptPinsVersionAndVerifies(t *testing.T) {
	s := InstallScript("0.155.0")
	assert.Contains(t, s, "ver='0.155.0'")
	assert.Contains(t, s, "otelcol-contrib_${ver}_linux_${arch}.deb")
	assert.Contains(t, s, "releases/download/v0.155.0")
	assert.Contains(t, s, ChecksumsAsset)
	// Verifies the checksum and installs the local .deb keeping our config.
	assert.Contains(t, s, "checksum mismatch")
	assert.Contains(t, s, "--force-confold")
	// Idempotent skip when already at this version.
	assert.Contains(t, s, "already installed")

	// Regression (v5.0.1 deploy): the downloaded package MUST keep its .deb name —
	// `apt-get install <file>` rejects a bare mktemp path with "Unsupported file".
	assert.Contains(t, s, `deb="$tmpd/${asset}"`, "download target must keep the .deb filename")
	assert.Contains(t, s, `apt-get -o DPkg::Lock::Timeout=300 install -y `, "apt must wait for the lock and install the .deb")
	assert.Contains(t, s, `"$deb"`, "apt-get install target is the .deb-named file")
	assert.NotContains(t, s, `-o "$tmp"`, "must not install a bare (non-.deb) mktemp file")
}

// TestInstallScriptParses runs the rendered install shell through `bash -n`.
func TestInstallScriptParses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	p := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(p, []byte(InstallScript("0.155.0")), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bash, "-n", p).CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func TestCredentialScriptIsOwnedAndPrivate(t *testing.T) {
	s := CredentialScript("YWJjOnRrbg==")
	assert.Contains(t, s, CredentialPath)
	assert.Contains(t, s, "chmod '0600'")
	assert.Contains(t, s, "chown '"+User+":"+User+"'")
	// Written via temp + atomic mv (install /dev/stdin over an existing file fails on
	// coreutils 9 / Ubuntu 26.04).
	assert.Contains(t, s, "mktemp")
	assert.Contains(t, s, "mv \"$tmp\" ")
	// The value travels base64'd, decoded on the host — never as plaintext argv.
	assert.Contains(t, s, "base64 -d")
}

func TestApplyScriptEnablesAndRestarts(t *testing.T) {
	cfg, err := Render("https://otlp.example/v1", fullAttrs())
	require.NoError(t, err)
	s := ApplyScript(cfg)
	assert.Contains(t, s, ConfigPath)
	assert.Contains(t, s, "systemctl enable '"+Service+"'")
	assert.Contains(t, s, "systemctl restart '"+Service+"'")
	// The config body travels base64-encoded (decoded on the host), so the rendered
	// bytes appear as their base64, not as plaintext.
	assert.True(t, strings.Contains(s, base64Encode(cfg)))
}
