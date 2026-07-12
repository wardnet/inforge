package cloudinit

import (
	"os"
	"os/exec"
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

// assignments returns the DEPLOY_USER/DEPLOY_KEY assignment lines of a rendered
// provision script — the part a hostile value would break out of.
func assignments(t *testing.T, script string) string {
	t.Helper()
	start := strings.Index(script, "DEPLOY_USER=")
	require.GreaterOrEqual(t, start, 0, "DEPLOY_USER assignment must be present")
	end := strings.Index(script[start:], "\nif [ -n ")
	require.GreaterOrEqual(t, end, 0, "the guard following the assignments must be present")
	return script[start:start+end] + "\n"
}

// The deploy user and public key are substituted into single-quoted assignments in
// a script bash runs AS ROOT at first boot: a value carrying a quote must stay data.
func TestProvisionEscapesHostileDeployMaterial(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cases := []struct{ name, user, key string }{
		{"embedded quote", `deploy'; touch MARKER; x='`, "ssh-ed25519 AAAA"},
		{"command substitution", `deploy'; $(touch MARKER)'`, `ssh-ed25519 AAAA'$(touch MARKER)'`},
		{"backtick", "deploy'`touch MARKER`'", "ssh-ed25519 AAAA"},
		{"newline", "deploy'\ntouch MARKER\n'", "ssh-ed25519 AAAA\nssh-rsa BBBB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "MARKER")
			user := strings.ReplaceAll(c.user, "MARKER", marker)
			key := strings.ReplaceAll(c.key, "MARKER", marker)

			script := assignments(t, ProvisionOnly(Vars{DeployUser: user, DeployPublicKey: key})) +
				"printf '%s' \"$DEPLOY_USER\" > " + filepath.Join(dir, "user") + "\n" +
				"printf '%s' \"$DEPLOY_KEY\" > " + filepath.Join(dir, "key") + "\n"
			path := filepath.Join(dir, "provision.sh")
			require.NoError(t, os.WriteFile(path, []byte(script), 0o700)) // #nosec G306

			out, err := exec.Command(bash, path).CombinedOutput()
			require.NoError(t, err, "rendered script must be valid shell: %s", out)

			assert.NoFileExists(t, marker, "the injected command must not run")
			gotUser, err := os.ReadFile(filepath.Join(dir, "user"))
			require.NoError(t, err)
			assert.Equal(t, user, string(gotUser), "the value must survive as data")
			gotKey, err := os.ReadFile(filepath.Join(dir, "key"))
			require.NoError(t, err)
			assert.Equal(t, key, string(gotKey), "the value must survive as data")
		})
	}
}

func TestVarsValidate(t *testing.T) {
	const goodKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdef+/0123456789 op@host"
	cases := []struct {
		name    string
		vars    Vars
		wantErr string
	}{
		{"valid", Vars{DeployUser: "deploy", DeployPublicKey: goodKey}, ""},
		{"no deploy user", Vars{}, ""},
		{"quote in user", Vars{DeployUser: `deploy'; touch /x; '`, DeployPublicKey: goodKey}, "valid login name"},
		{"uppercase user", Vars{DeployUser: "Deploy", DeployPublicKey: goodKey}, "valid login name"},
		{"leading digit user", Vars{DeployUser: "1deploy", DeployPublicKey: goodKey}, "valid login name"},
		{"multi-line key", Vars{DeployUser: "deploy", DeployPublicKey: goodKey + "\nssh-rsa AAAA attacker"}, "single-line OpenSSH public key"},
		{"garbage key", Vars{DeployUser: "deploy", DeployPublicKey: "not-a-key"}, "single-line OpenSSH public key"},
		{"trailing newline tolerated", Vars{DeployUser: "deploy", DeployPublicKey: goodKey + "\n"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.vars.Validate()
			if c.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.wantErr)
		})
	}
}
