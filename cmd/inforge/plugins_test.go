package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mirrorDigest must resolve the entry for the requested file and nothing else —
// a SHA256SUMS covers every mirrored platform, so picking the wrong line would
// install a different platform's binary or fail confusingly.
func TestMirrorDigestSelectsTheRequestedFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(
			"aaaa  pulumi-resource-hcloud-v1.38.0-linux-amd64.tar.gz\n" +
				"bbbb  pulumi-resource-hcloud-v1.38.0-linux-arm64.tar.gz\n" +
				"cccc  pulumi-resource-hcloud-v1.38.0-darwin-arm64.tar.gz\n"))
	}))
	defer srv.Close()

	got, err := mirrorDigest(context.Background(), srv.URL, "pulumi-resource-hcloud-v1.38.0-linux-arm64.tar.gz")
	if err != nil {
		t.Fatalf("mirrorDigest: %v", err)
	}
	if got != "bbbb" {
		t.Errorf("digest = %q, want %q", got, "bbbb")
	}
}

// A file absent from SHA256SUMS must be a hard error. Falling back to installing
// it unverified would silently restore the behaviour this change removes.
func TestMirrorDigestRejectsMissingEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("aaaa  some-other-file.tar.gz\n"))
	}))
	defer srv.Close()

	if _, err := mirrorDigest(context.Background(), srv.URL, "wanted.tar.gz"); err == nil {
		t.Fatal("expected an error for a file with no published digest, got nil")
	}
}

// An unmirrored version must name the mirror in the error — the fix is always
// "mirror it first", and that is not guessable from a bare 404.
func TestMirrorDigestReportsUnmirroredVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := mirrorDigest(context.Background(), srv.URL, "anything.tar.gz")
	if err == nil {
		t.Fatal("expected an error for a missing SHA256SUMS, got nil")
	}
	if !strings.Contains(err.Error(), mirrorRepo) {
		t.Errorf("error should point at the mirror, got: %v", err)
	}
}

// The whole point of the change: bytes that do not match the published digest are
// never extracted. A tampered or corrupt archive must fail before anything is
// written to the plugin directory.
func TestDownloadAndExtractRejectsChecksumMismatch(t *testing.T) {
	payload := []byte("not really a tarball")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	real := sha256.Sum256(payload)
	wrong := strings.Repeat("0", 64)

	err := downloadAndExtractTarGzVerified(context.Background(), srv.URL, t.TempDir(), "pulumi-resource-x", wrong)
	if err == nil {
		t.Fatal("expected a checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("want a checksum mismatch error, got: %v", err)
	}
	// The real digest must not leak into the failure as though it were expected.
	if strings.Contains(err.Error(), hex.EncodeToString(real[:])) && !strings.Contains(err.Error(), wrong) {
		t.Error("error should report both wanted and got digests")
	}
}

// tarGzWith builds a gzipped tar containing one entry, as the provider archives
// do, so the extraction path can be exercised without the network.
func tarGzWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// The happy path: a correctly-signed archive is extracted and the binary lands
// executable in the plugin directory.
func TestDownloadAndExtractVerifiedExtractsBinary(t *testing.T) {
	const binary = "pulumi-resource-hcloud"
	want := []byte("#!/bin/sh\necho plugin\n")
	archive := tarGzWith(t, binary, want)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	sum := sha256.Sum256(archive)
	dir := t.TempDir()

	if err := downloadAndExtractTarGzVerified(context.Background(), srv.URL, dir, binary, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("extract: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, binary))
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted content = %q, want %q", got, want)
	}

	info, err := os.Stat(filepath.Join(dir, binary))
	if err != nil {
		t.Fatal(err)
	}
	// Pulumi execs the plugin directly; a non-executable file fails at deploy time.
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("extracted binary is not executable (mode %v)", info.Mode().Perm())
	}
}

// An archive that verifies but does not contain the expected binary must error
// rather than silently leave the plugin directory empty — the deploy would then
// fail much later with a confusing "plugin not found".
func TestDownloadAndExtractVerifiedRejectsArchiveWithoutBinary(t *testing.T) {
	archive := tarGzWith(t, "some-other-file", []byte("x"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	sum := sha256.Sum256(archive)
	err := downloadAndExtractTarGzVerified(context.Background(), srv.URL, t.TempDir(), "pulumi-resource-hcloud", hex.EncodeToString(sum[:]))
	if err == nil {
		t.Fatal("expected an error when the binary is absent from the archive, got nil")
	}
	if !strings.Contains(err.Error(), "not found in archive") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A 404 from the mirror must name the mirror: the fix is always "mirror that
// version first", which is not guessable from a bare HTTP status.
func TestDownloadAndExtractVerifiedReportsUnmirroredArtifact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := downloadAndExtractTarGzVerified(context.Background(), srv.URL, t.TempDir(), "pulumi-resource-hcloud", strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("expected an error on 404, got nil")
	}
	if !strings.Contains(err.Error(), mirrorRepo) {
		t.Errorf("error should point at the mirror, got: %v", err)
	}
}

// The two naming conventions are easy to conflate and fail as an unhelpful 404,
// so pin them: provider archives use GOARCH (amd64), while the Pulumi CLI's own
// archives use x64 for the same machine.
func TestPluginArchiveNameUsesGoarchSpelling(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
	}{
		{"linux", "amd64", "pulumi-resource-hcloud-v1.38.0-linux-amd64.tar.gz"},
		{"linux", "arm64", "pulumi-resource-hcloud-v1.38.0-linux-arm64.tar.gz"},
		{"darwin", "arm64", "pulumi-resource-hcloud-v1.38.0-darwin-arm64.tar.gz"},
	}
	for _, tt := range tests {
		if got := pluginArchiveName("hcloud", "1.38.0", tt.goos, tt.goarch); got != tt.want {
			t.Errorf("pluginArchiveName(%s/%s) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

// The tag scheme is the mirror's contract; producer and consumer must agree.
func TestMirrorPluginBaseMatchesTheMirrorTagScheme(t *testing.T) {
	want := "https://github.com/wardnet/toolchain-mirror/releases/download/plugin-grafana-v1.0.0"
	if got := mirrorPluginBase("grafana", "1.0.0"); got != want {
		t.Errorf("mirrorPluginBase = %q, want %q", got, want)
	}
}
