package cloudinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const template = `#!/bin/bash
host {{domain}}
instance {{instance}}
key {{deploy_public_key}}
user {{deploy_user}}
manifest:
{{manifest}}
`

func TestRender(t *testing.T) {
	out := Render(template, Vars{
		Domain:          "bridge.use1.example.com",
		DeployPublicKey: "ssh-ed25519 AAAA",
		DeployUser:      "deploy",
		Instance:        2,
		Manifest:        "version: 1",
	})

	assert.Contains(t, out, "host bridge.use1.example.com")
	assert.Contains(t, out, "instance 2")
	assert.Contains(t, out, "key ssh-ed25519 AAAA")
	assert.Contains(t, out, "user deploy")
	assert.Contains(t, out, "version: 1")
	assert.NotContains(t, out, "{{", "all placeholders should be substituted")
	assert.Contains(t, out, "inforge user provisioning", "the provision step should be appended")
}

func TestRenderNoDeployUser(t *testing.T) {
	out := Render(template, Vars{
		Domain:   "bridge.use1.example.com",
		Instance: 1,
	})
	// DEPLOY_USER is empty; provision script should be present but do nothing.
	assert.Contains(t, out, "inforge user provisioning")
	assert.NotContains(t, out, "{{", "all placeholders should be substituted")
}

func TestAssemble(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tmpl.sh")
	require.NoError(t, os.WriteFile(path, []byte(template), 0o600))

	out, err := Assemble(path, Vars{Domain: "x.example.com", Instance: 1})
	require.NoError(t, err)
	assert.Contains(t, out, "host x.example.com")
	assert.Contains(t, out, "inforge user provisioning")

	_, err = Assemble(filepath.Join(dir, "missing.sh"), Vars{})
	assert.Error(t, err)
}

func TestProvisionScriptNonEmpty(t *testing.T) {
	assert.Contains(t, ProvisionScript(), "DEPLOY_USER=")
}

func TestProvisionOnly(t *testing.T) {
	out := ProvisionOnly(Vars{
		DeployPublicKey: "ssh-ed25519 AAAA",
		DeployUser:      "deploy",
	})
	// Must be a standalone script cloud-init executes (no project template
	// supplies the shebang here).
	assert.True(t, strings.HasPrefix(out, "#!/bin/bash\n"), "must start with a shebang")
	assert.Contains(t, out, "inforge user provisioning", "the provision step must be present")
	assert.Contains(t, out, "DEPLOY_USER='deploy'")
	assert.Contains(t, out, "ssh-ed25519 AAAA", "the deploy public key must be substituted in")
	assert.NotContains(t, out, "{{", "all placeholders should be substituted")
}
