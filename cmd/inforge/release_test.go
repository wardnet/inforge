package main

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	iapp "github.com/wardnet/inforge/internal/app"
	"github.com/wardnet/inforge/internal/release"
	"github.com/wardnet/inforge/internal/service"
)

// stubS3 is a minimal release.NewStoreWithAPI-satisfying fake: only getObject
// has real behavior (configurable per test), every other method errors —
// enough to unit test downloadArtifact (which calls only GetArtifact/
// GetObject) without a live bucket or a duplicate full S3 fake.
type stubS3 struct {
	getObject func(*s3.GetObjectInput) (*s3.GetObjectOutput, error)
}

func (s *stubS3) PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return nil, errors.New("stubS3: PutObject not implemented")
}
func (s *stubS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	return s.getObject(in)
}
func (s *stubS3) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	return nil, errors.New("stubS3: HeadObject not implemented")
}
func (s *stubS3) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	return nil, errors.New("stubS3: ListObjectsV2 not implemented")
}
func (s *stubS3) DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	return nil, errors.New("stubS3: DeleteObject not implemented")
}

// TestServiceApplyScript pins adapter #1's remote command — the refactor behind
// the delivery seam must keep the service path byte-for-byte: extract into the
// service folder, drop the payload, restart the unit, off the shared payload path.
func TestServiceApplyScript(t *testing.T) {
	got := serviceApplyScript(service.DeployTarget{Folder: "/srv/wardnet/api", Unit: "wardnet-api.service"})
	// Folder and unit (both name-derived) are single-quoted via remote.Quote, matching
	// the app path — no raw interpolation of a caller-supplied value into the shell.
	want := "sudo tar -xzf /tmp/inforge-payload.tgz -C '/srv/wardnet/api' && " +
		"rm -f /tmp/inforge-payload.tgz && " +
		"sudo systemctl restart 'wardnet-api.service'"
	assert.Equal(t, want, got)
}

// TestServiceDeliveryTargets: the SSHUser the descriptor resolved (always set —
// BuildDeployDescriptor defaults it to "deploy") is passed through, and the apply
// step's human label carries the folder + unit for operator visibility. Each
// target's arch and payload come from the probed-arch/downloaded-payload maps,
// keyed by host and by arch respectively — h1 and h2 are different archs here,
// so they must resolve to different payload files.
func TestServiceDeliveryTargets(t *testing.T) {
	archOf := map[string]string{"h1": "amd64", "h2": "arm64"}
	payloadOf := map[string]string{"amd64": "/tmp/amd64.tgz", "arm64": "/tmp/arm64.tgz"}
	dts := serviceDeliveryTargets([]service.DeployTarget{
		{HostDNS: "h1", Folder: "/srv/wardnet/api", Unit: "wardnet-api.service", SSHUser: "deploy"},
		{HostDNS: "h2", Folder: "/srv/wardnet/api", Unit: "u", SSHUser: "ops"},
	}, archOf, payloadOf)
	assert.Equal(t, "deploy", dts[0].sshUser)
	assert.Equal(t, "ops", dts[1].sshUser)
	assert.Equal(t, "h1", dts[0].host)
	assert.Equal(t, "extracting to /srv/wardnet/api and restarting wardnet-api.service", dts[0].describe)
	assert.Equal(t, "amd64", dts[0].arch)
	assert.Equal(t, "/tmp/amd64.tgz", dts[0].payloadFile)
	assert.Equal(t, "arm64", dts[1].arch)
	assert.Equal(t, "/tmp/arm64.tgz", dts[1].payloadFile)
}

// TestAppReleaseScript pins adapter #2's fresh-release command: extract the bundle
// into its per-SHA dir, atomically swap `current`, validate + reload nginx, GC.
func TestAppReleaseScript(t *testing.T) {
	got := appReleaseScript(iapp.DeployTarget{App: "my"}, "abc123")
	assert.Contains(t, got, "sudo mkdir -p '/srv/wardnet/app/my/abc123'")
	assert.Contains(t, got, "sudo tar -xzf /tmp/inforge-payload.tgz -C '/srv/wardnet/app/my/abc123'")
	assert.Contains(t, got, "sudo mv -T '/srv/wardnet/app/my/.current.tmp' '/srv/wardnet/app/my/current'")
	assert.Contains(t, got, "sudo nginx -t")
	assert.Contains(t, got, "sudo systemctl reload nginx")
	assert.Contains(t, got, "xargs -r sudo rm -rf")
	// Validate (nginx -t) BEFORE the swap, so a config error can't leave `current`
	// swapped without a reload; the swap precedes the reload, which precedes the GC.
	assert.Less(t, idx(got, "nginx -t"), idx(got, "mv -T"))
	assert.Less(t, idx(got, "mv -T"), idx(got, "reload nginx"))
	assert.Less(t, idx(got, "reload nginx"), idx(got, "rm -rf"))
}

// TestAppRollbackScript: rollback asserts the bundle is present, swaps + reloads,
// and never extracts or fetches.
func TestAppRollbackScript(t *testing.T) {
	got := appRollbackScript(iapp.DeployTarget{App: "my"}, "abc123")
	assert.Contains(t, got, "test -d '/srv/wardnet/app/my/abc123'")
	assert.Contains(t, got, "sudo mv -T '/srv/wardnet/app/my/.current.tmp' '/srv/wardnet/app/my/current'")
	assert.Contains(t, got, "sudo nginx -t")
	assert.Contains(t, got, "sudo systemctl reload nginx")
	// Validate before the swap here too.
	assert.Less(t, idx(got, "nginx -t"), idx(got, "mv -T"))
	assert.Less(t, idx(got, "mv -T"), idx(got, "reload nginx"))
	assert.NotContains(t, got, "tar -xzf")
	assert.NotContains(t, got, "mkdir")
}

// TestAppDeliveryTargets: fresh vs rollback choose the right apply script, the
// SSH user defaults to "deploy", apps are always architecture-agnostic (arch
// ""), and rollback never carries a payload (no upload — the bundle is
// already on the host) regardless of what's passed in.
func TestAppDeliveryTargets(t *testing.T) {
	targets := []iapp.DeployTarget{{App: "my", IngressHostDNS: "edge.example.com"}}

	fresh := appDeliveryTargets(targets, "abc123", "/tmp/bundle.tgz", false)
	assert.Equal(t, "edge.example.com", fresh[0].host)
	assert.Contains(t, fresh[0].applyScript, "tar -xzf")
	assert.Equal(t, "releasing /srv/wardnet/app/my/abc123", fresh[0].describe)
	assert.Empty(t, fresh[0].arch)
	assert.Equal(t, "/tmp/bundle.tgz", fresh[0].payloadFile)

	roll := appDeliveryTargets(targets, "abc123", "/tmp/bundle.tgz", true)
	assert.Contains(t, roll[0].applyScript, "test -d")
	assert.NotContains(t, roll[0].applyScript, "tar -xzf")
	assert.Equal(t, "rolling back /srv/wardnet/app/my/abc123", roll[0].describe)
	assert.Empty(t, roll[0].payloadFile, "rollback never uploads, regardless of the payload passed in")
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

// withFakeProbeSSH swaps runProbeSSH for the duration of the test, restoring
// the real exec-backed implementation on cleanup.
func withFakeProbeSSH(t *testing.T, fake func(ctx context.Context, args []string) ([]byte, error)) {
	t.Helper()
	orig := runProbeSSH
	runProbeSSH = fake
	t.Cleanup(func() { runProbeSSH = orig })
}

// TestProbeHostArchMapsUname: a successful `uname -m` output is mapped to the
// Go/goreleaser arch vocabulary.
func TestProbeHostArchMapsUname(t *testing.T) {
	var gotArgs []string
	withFakeProbeSSH(t, func(_ context.Context, args []string) ([]byte, error) {
		gotArgs = args
		return []byte("x86_64\n"), nil
	})
	arch, err := probeHostArch(context.Background(), "h1.example.com", "deploy", "/tmp/key")
	require.NoError(t, err)
	assert.Equal(t, "amd64", arch)
	assert.Contains(t, gotArgs, "deploy@h1.example.com")
	assert.Contains(t, gotArgs, "uname -m")
}

// TestProbeHostArchUnsupported: an unrecognized `uname -m` output surfaces
// mapUnameArch's error rather than a bogus arch.
func TestProbeHostArchUnsupported(t *testing.T) {
	withFakeProbeSSH(t, func(context.Context, []string) ([]byte, error) { return []byte("mips\n"), nil })
	_, err := probeHostArch(context.Background(), "h1", "deploy", "/tmp/key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mips")
}

// TestProbeHostArchSSHError: an SSH transport failure is wrapped with the
// host name.
func TestProbeHostArchSSHError(t *testing.T) {
	boom := errors.New("boom")
	withFakeProbeSSH(t, func(context.Context, []string) ([]byte, error) { return nil, boom })
	_, err := probeHostArch(context.Background(), "h1", "deploy", "/tmp/key")
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "h1")
}

// TestProbeTargetArchsGroupsByArch: every target is probed and the reverse
// index (arch -> hosts) groups hosts sharing an arch together.
func TestProbeTargetArchsGroupsByArch(t *testing.T) {
	withFakeProbeSSH(t, func(_ context.Context, args []string) ([]byte, error) {
		if slices.Contains(args, "deploy@h2") {
			return []byte("aarch64\n"), nil
		}
		return []byte("x86_64\n"), nil
	})
	targets := []service.DeployTarget{
		{HostDNS: "h1", SSHUser: "deploy"},
		{HostDNS: "h2", SSHUser: "deploy"},
		{HostDNS: "h3", SSHUser: "deploy"},
	}
	archOf, hostsByArch, err := probeTargetArchs(context.Background(), targets, "/tmp/key")
	require.NoError(t, err)
	assert.Equal(t, "amd64", archOf["h1"])
	assert.Equal(t, "arm64", archOf["h2"])
	assert.Equal(t, "amd64", archOf["h3"])
	assert.ElementsMatch(t, []string{"h1", "h3"}, hostsByArch["amd64"])
	assert.ElementsMatch(t, []string{"h2"}, hostsByArch["arm64"])
}

// TestProbeTargetArchsPropagatesError: a probe failure on any host aborts the
// whole probe with that host named in the error.
func TestProbeTargetArchsPropagatesError(t *testing.T) {
	withFakeProbeSSH(t, func(context.Context, []string) ([]byte, error) {
		return nil, errors.New("connection refused")
	})
	targets := []service.DeployTarget{{HostDNS: "h1", SSHUser: "deploy"}}
	_, _, err := probeTargetArchs(context.Background(), targets, "/tmp/key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h1")
}

// TestVerifyArchsPushedAllPresent: every distinct arch across the probed
// fleet exists in the store, so the resolution carries the probed archOf
// through unchanged and a sorted archList.
func TestVerifyArchsPushedAllPresent(t *testing.T) {
	archOf := map[string]string{"h1": "amd64", "h2": "arm64"}
	hostsByArch := map[string][]string{"amd64": {"h1"}, "arm64": {"h2"}}
	res, err := verifyArchsPushed(context.Background(), "api", "sha1", archOf, hostsByArch,
		func(context.Context, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	assert.Equal(t, archOf, res.archOf)
	assert.Equal(t, []string{"amd64", "arm64"}, res.archList)
}

// TestVerifyArchsPushedMissingArch: a needed arch has no pushed artifact —
// the error must name the affected host(s), the detected arch, and the exact
// R2 key looked for, so an operator can act on it directly.
func TestVerifyArchsPushedMissingArch(t *testing.T) {
	archOf := map[string]string{"h1": "amd64", "h2": "arm64"}
	hostsByArch := map[string][]string{"amd64": {"h1"}, "arm64": {"h2"}}
	_, err := verifyArchsPushed(context.Background(), "api", "sha1", archOf, hostsByArch,
		func(_ context.Context, arch string) (bool, error) { return arch != "arm64", nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h2")
	assert.Contains(t, err.Error(), "arm64")
	assert.Contains(t, err.Error(), release.ArtifactKey("api", "sha1", "arm64"))
	assert.Contains(t, err.Error(), "inforge releases push --arch arm64")
}

// TestVerifyArchsPushedCheckerError: a store error while checking existence
// aborts immediately rather than being swallowed.
func TestVerifyArchsPushedCheckerError(t *testing.T) {
	boom := errors.New("boom")
	_, err := verifyArchsPushed(context.Background(), "api", "sha1",
		map[string]string{"h1": "amd64"}, map[string][]string{"amd64": {"h1"}},
		func(context.Context, string) (bool, error) { return false, boom })
	require.ErrorIs(t, err, boom)
}

// TestDownloadArchPayloadsFromDownloadsOncePerArch: two hosts sharing one
// arch must trigger exactly one download for that arch (never once per
// host), and the resulting delivery targets carry the shared payload.
func TestDownloadArchPayloadsFromDownloadsOncePerArch(t *testing.T) {
	calls := map[string]int{}
	targets := []service.DeployTarget{
		{HostDNS: "h1", Folder: "/srv/wardnet/api", Unit: "u", SSHUser: "deploy"},
		{HostDNS: "h2", Folder: "/srv/wardnet/api", Unit: "u", SSHUser: "deploy"},
	}
	res := archResolution{
		archOf:   map[string]string{"h1": "amd64", "h2": "amd64"},
		archList: []string{"amd64"},
	}
	dts, cleanup, err := downloadArchPayloadsFrom(context.Background(), targets, res,
		func(_ context.Context, arch string) (string, func(), error) {
			calls[arch]++
			return "/tmp/" + arch + ".tgz", func() {}, nil
		})
	require.NoError(t, err)
	defer cleanup()
	assert.Equal(t, 1, calls["amd64"])
	assert.Equal(t, "/tmp/amd64.tgz", dts[0].payloadFile)
	assert.Equal(t, "/tmp/amd64.tgz", dts[1].payloadFile)
}

// TestDownloadArchPayloadsFromErrorRunsCleanup: a download failure on the
// second arch must still release the first arch's temp file (no leak) and
// return the error with no delivery targets.
func TestDownloadArchPayloadsFromErrorRunsCleanup(t *testing.T) {
	var cleaned []string
	boom := errors.New("boom")
	res := archResolution{archList: []string{"amd64", "arm64"}}
	dts, cleanup, err := downloadArchPayloadsFrom(context.Background(), nil, res,
		func(_ context.Context, arch string) (string, func(), error) {
			if arch == "arm64" {
				return "", func() {}, boom
			}
			return "/tmp/" + arch + ".tgz", func() { cleaned = append(cleaned, arch) }, nil
		})
	require.ErrorIs(t, err, boom)
	assert.Nil(t, dts)
	cleanup()
	assert.Equal(t, []string{"amd64"}, cleaned, "the already-downloaded amd64 payload must be cleaned up even though arm64 failed")
}

// TestAnyArchPushedNoneFound: no supported arch has a pushed artifact.
func TestAnyArchPushedNoneFound(t *testing.T) {
	found, err := anyArchPushed(context.Background(), "api", "sha1",
		func(context.Context, string) (bool, error) { return false, nil })
	require.NoError(t, err)
	assert.Empty(t, found)
}

// TestAnyArchPushedSomeFound: only amd64 was pushed — its key is reported.
func TestAnyArchPushedSomeFound(t *testing.T) {
	found, err := anyArchPushed(context.Background(), "api", "sha1",
		func(_ context.Context, arch string) (bool, error) { return arch == "amd64", nil })
	require.NoError(t, err)
	assert.Equal(t, []string{release.ArtifactKey("api", "sha1", "amd64")}, found)
}

// TestAnyArchPushedCheckerError: a store error aborts the scan.
func TestAnyArchPushedCheckerError(t *testing.T) {
	boom := errors.New("boom")
	_, err := anyArchPushed(context.Background(), "api", "sha1",
		func(context.Context, string) (bool, error) { return false, boom })
	require.ErrorIs(t, err, boom)
}

// TestDryRunWithoutSSHFound: at least one arch is pushed — the reduced dry-run
// check passes.
func TestDryRunWithoutSSHFound(t *testing.T) {
	err := dryRunWithoutSSH(context.Background(), "api", "sha1", errors.New("no key configured"),
		func(_ context.Context, arch string) (bool, error) { return arch == "amd64", nil })
	assert.NoError(t, err)
}

// TestDryRunWithoutSSHNoneFound: no arch has been pushed for this SHA — the
// dry-run must fail loudly with a hint to push one.
func TestDryRunWithoutSSHNoneFound(t *testing.T) {
	err := dryRunWithoutSSH(context.Background(), "api", "sha1", errors.New("no key configured"),
		func(context.Context, string) (bool, error) { return false, nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "releases push --arch")
}

// TestDryRunWithoutSSHCheckerError: a store error propagates.
func TestDryRunWithoutSSHCheckerError(t *testing.T) {
	boom := errors.New("boom")
	err := dryRunWithoutSSH(context.Background(), "api", "sha1", errors.New("no key"),
		func(context.Context, string) (bool, error) { return false, boom })
	require.ErrorIs(t, err, boom)
}

// TestFormatReleaseListRowTruncatesSHA: a full-length SHA is truncated to 12
// chars for the fixed-width table.
func TestFormatReleaseListRowTruncatesSHA(t *testing.T) {
	d := release.Deployment{SHA: "0123456789abcdef", Arch: "amd64", DeployedAt: fixedTime(t)}
	row := formatReleaseListRow("h1.example.com", d)
	assert.Contains(t, row, "0123456789ab")
	assert.NotContains(t, row, "0123456789abcdef")
	assert.Contains(t, row, "amd64")
}

// TestFormatReleaseListRowDefaultsEmptyArch: an app deployment or a manifest
// entry predating arch-awareness has an empty Arch, rendered as "-".
func TestFormatReleaseListRowDefaultsEmptyArch(t *testing.T) {
	d := release.Deployment{SHA: "abc123", DeployedAt: fixedTime(t)}
	row := formatReleaseListRow("h1", d)
	assert.Contains(t, row, "-")
}

// TestReleaseListHeaderColumns: the header names every column
// formatReleaseListRow fills in, in the same order.
func TestReleaseListHeaderColumns(t *testing.T) {
	header := releaseListHeader()
	assert.Contains(t, header, "HOST")
	assert.Contains(t, header, "SHA")
	assert.Contains(t, header, "ARCH")
	assert.Contains(t, header, "DEPLOYED")
	assert.Less(t, idx(header, "HOST"), idx(header, "SHA"))
	assert.Less(t, idx(header, "SHA"), idx(header, "ARCH"))
	assert.Less(t, idx(header, "ARCH"), idx(header, "DEPLOYED"))
}

// TestDownloadArtifactWritesTempFile: a successful download writes the
// artifact's body to a temp file whose content matches, and cleanup removes
// it.
func TestDownloadArtifactWritesTempFile(t *testing.T) {
	const body = "fake tarball bytes"
	store := release.NewStoreWithAPI(&stubS3{
		getObject: func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
		},
	}, "bucket")

	payload, cleanup, err := downloadArtifact(context.Background(), store, "api", "sha1", "amd64")
	require.NoError(t, err)
	defer cleanup()

	got, err := os.ReadFile(payload)
	require.NoError(t, err)
	assert.Equal(t, body, string(got))

	cleanup()
	_, statErr := os.Stat(payload)
	assert.True(t, os.IsNotExist(statErr), "cleanup must remove the temp file")
}

// TestDownloadArtifactStoreError: a GetArtifact failure (e.g. not pushed)
// propagates without creating a temp file.
func TestDownloadArtifactStoreError(t *testing.T) {
	boom := errors.New("not found")
	store := release.NewStoreWithAPI(&stubS3{
		getObject: func(*s3.GetObjectInput) (*s3.GetObjectOutput, error) { return nil, boom },
	}, "bucket")

	_, _, err := downloadArtifact(context.Background(), store, "api", "sha1", "amd64")
	require.Error(t, err)
}

func fixedTime(t *testing.T) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, "2026-07-10T12:00:00Z")
	require.NoError(t, err)
	return tm
}
