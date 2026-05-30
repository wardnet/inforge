package cloudinit

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const template = `#!/bin/bash
host {{domain}}
instance {{instance}}
key {{deploy_public_key}}
manifest:
{{manifest}}
`

func TestRender(t *testing.T) {
	out := Render(template, Vars{
		Domain:          "bridge.use1.example.com",
		DeployPublicKey: "ssh-ed25519 AAAA",
		Instance:        2,
		Manifest:        "version: 1",
	})

	assert.Contains(t, out, "host bridge.use1.example.com")
	assert.Contains(t, out, "instance 2")
	assert.Contains(t, out, "key ssh-ed25519 AAAA")
	assert.Contains(t, out, "version: 1")
	assert.NotContains(t, out, "{{", "all placeholders should be substituted")
	assert.Contains(t, out, "inforge bootstrap", "the bootstrap step should be appended")
}

func TestAssemble(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tmpl.sh")
	require.NoError(t, os.WriteFile(path, []byte(template), 0o600))

	out, err := Assemble(path, Vars{Domain: "x.example.com", Instance: 1})
	require.NoError(t, err)
	assert.Contains(t, out, "host x.example.com")
	assert.Contains(t, out, "inforge bootstrap")

	_, err = Assemble(filepath.Join(dir, "missing.sh"), Vars{})
	assert.Error(t, err)
}

func TestBootstrapScriptNonEmpty(t *testing.T) {
	assert.Contains(t, BootstrapScript(), "BOOTSTRAP_FILE=")
}
