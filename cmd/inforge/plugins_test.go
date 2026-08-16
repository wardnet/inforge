package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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
