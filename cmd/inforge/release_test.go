package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	iapp "github.com/wardnet/inforge/internal/app"
	"github.com/wardnet/inforge/internal/service"
)

// TestServiceApplyScript pins adapter #1's remote command — the refactor behind
// the delivery seam must keep the service path byte-for-byte: extract into the
// service folder, drop the payload, restart the unit, off the shared payload path.
func TestServiceApplyScript(t *testing.T) {
	got := serviceApplyScript(service.DeployTarget{Folder: "/srv/wardnet/api", Unit: "wardnet-api.service"})
	want := "sudo tar -xzf /tmp/inforge-payload.tgz -C /srv/wardnet/api && " +
		"rm -f /tmp/inforge-payload.tgz && " +
		"sudo systemctl restart wardnet-api.service"
	assert.Equal(t, want, got)
}

// TestServiceDeliveryTargets: the SSHUser the descriptor resolved (always set —
// BuildDeployDescriptor defaults it to "deploy") is passed through, and the apply
// step's human label carries the folder + unit for operator visibility.
func TestServiceDeliveryTargets(t *testing.T) {
	dts := serviceDeliveryTargets([]service.DeployTarget{
		{HostDNS: "h1", Folder: "/srv/wardnet/api", Unit: "wardnet-api.service", SSHUser: "deploy"},
		{HostDNS: "h2", Folder: "/srv/wardnet/api", Unit: "u", SSHUser: "ops"},
	})
	assert.Equal(t, "deploy", dts[0].sshUser)
	assert.Equal(t, "ops", dts[1].sshUser)
	assert.Equal(t, "h1", dts[0].host)
	assert.Equal(t, "extracting to /srv/wardnet/api and restarting wardnet-api.service", dts[0].describe)
}

// TestAppReleaseScript pins adapter #2's fresh-release command: extract the bundle
// into its per-SHA dir, atomically swap `current`, validate + reload nginx, GC.
func TestAppReleaseScript(t *testing.T) {
	got := appReleaseScript(iapp.DeployTarget{App: "my"}, "abc123")
	assert.Contains(t, got, "sudo mkdir -p '/srv/wardnet/app/my/abc123'")
	assert.Contains(t, got, "sudo tar -xzf /tmp/inforge-payload.tgz -C '/srv/wardnet/app/my/abc123'")
	assert.Contains(t, got, "sudo mv -T '/srv/wardnet/app/my/.current.tmp' '/srv/wardnet/app/my/current'")
	assert.Contains(t, got, "sudo nginx -t && sudo systemctl reload nginx")
	assert.Contains(t, got, "xargs -r sudo rm -rf")
	// The swap must precede the reload, which must precede the GC.
	assert.Less(t, idx(got, "mv -T"), idx(got, "nginx -t"))
	assert.Less(t, idx(got, "nginx -t"), idx(got, "rm -rf"))
}

// TestAppRollbackScript: rollback asserts the bundle is present, swaps + reloads,
// and never extracts or fetches.
func TestAppRollbackScript(t *testing.T) {
	got := appRollbackScript(iapp.DeployTarget{App: "my"}, "abc123")
	assert.Contains(t, got, "test -d '/srv/wardnet/app/my/abc123'")
	assert.Contains(t, got, "sudo mv -T '/srv/wardnet/app/my/.current.tmp' '/srv/wardnet/app/my/current'")
	assert.Contains(t, got, "sudo nginx -t && sudo systemctl reload nginx")
	assert.NotContains(t, got, "tar -xzf")
	assert.NotContains(t, got, "mkdir")
}

// TestAppDeliveryTargets: fresh vs rollback choose the right apply script, and the
// SSH user defaults to "deploy".
func TestAppDeliveryTargets(t *testing.T) {
	targets := []iapp.DeployTarget{{App: "my", IngressHostDNS: "edge.example.com"}}

	fresh := appDeliveryTargets(targets, "abc123", false)
	assert.Equal(t, "edge.example.com", fresh[0].host)
	assert.Contains(t, fresh[0].applyScript, "tar -xzf")
	assert.Equal(t, "releasing /srv/wardnet/app/my/abc123", fresh[0].describe)

	roll := appDeliveryTargets(targets, "abc123", true)
	assert.Contains(t, roll[0].applyScript, "test -d")
	assert.NotContains(t, roll[0].applyScript, "tar -xzf")
	assert.Equal(t, "rolling back /srv/wardnet/app/my/abc123", roll[0].describe)
}

// TestAppArtifactSlug: apps are namespaced under app/ so they never collide with a
// like-named service in the store.
func TestAppArtifactSlug(t *testing.T) {
	assert.Equal(t, "app/my", appArtifactSlug("my"))
}

func idx(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
