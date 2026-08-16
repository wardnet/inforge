package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// maxPluginBinarySize bounds how much a single plugin binary download/extraction
// may write to disk. A generous cap well above any real Pulumi provider binary
// (tens of MB), but enough to stop a compromised or MITM'd release asset from
// exhausting disk space via an unbounded copy.
const maxPluginBinarySize = 500 << 20 // 500 MiB

func newPluginsCmd() *cobra.Command {
	plugins := &cobra.Command{
		Use:   "plugins",
		Short: "Manage Pulumi provider plugins",
	}

	install := &cobra.Command{
		Use:           "install",
		Short:         "Install all required Pulumi provider plugins",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			// Provider plugins come from OUR mirror, not from each provider's own
			// GitHub releases. Versions are pinned to match the SDK modules in go.mod.
			// See installPulumiPlugin for why the source moved.
			type stdPlugin struct{ name, version string }
			for _, p := range []stdPlugin{
				{"hcloud", "1.38.0"},
				{"cloudflare", "6.17.0"},
				// pulumi-random backs stable per-service database passwords (ADR-0036).
				{"random", "4.16.8"},
				// pulumiverse/grafana pushes dashboards + alerts (ADR-0038).
				{"grafana", "1.0.0"},
			} {
				fmt.Printf("installing pulumi-resource-%s v%s...\n", p.name, p.version)
				if err := installPulumiPlugin(ctx, p.name, p.version); err != nil {
					return fmt.Errorf("install %s: %w", p.name, err)
				}
				fmt.Printf("  installed pulumi-resource-%s\n", p.name)
			}

			// No custom (raw-binary) providers ship today: ADR-0036 retired the Neon
			// plugin and self-hosted Postgres needs none. The seam remains if one returns.

			fmt.Println("all plugins installed")
			return nil
		},
	}

	plugins.AddCommand(install)
	return plugins
}

// mirrorRepo is the release host for every third-party binary the toolchain
// installs. Mirroring is what makes a pinned version actually pinned: a pin stops
// us from silently moving, but the artifact still lived on someone else's host and
// could change or disappear underneath it. Add a version there before pinning it
// here — see that repo's README.
const mirrorRepo = "wardnet/toolchain-mirror"

// installPulumiPlugin downloads a Pulumi provider archive from our mirror,
// verifies it against the mirror's SHA256SUMS, and extracts the binary into the
// Pulumi plugins directory.
//
// Previously this fetched straight from each provider's GitHub releases with no
// verification at all — whatever bytes arrived were extracted and executed as part
// of a production deploy. Two things changed:
//
//   - the source is our mirror, so a pinned version cannot change or vanish;
//   - the download is verified, because fetching from a mirror without checking
//     the digest just relocates the trust instead of establishing it.
//
// Note the digest is SHA-256 even though the upstream provider repos publish only
// SHA-1: the mirror computes its own over the bytes it stored, so what we verify
// here is stronger than anything upstream offers for these artifacts.
func installPulumiPlugin(ctx context.Context, name, ver string) error {
	binary := "pulumi-resource-" + name

	// Provider archives use hyphen-separated os-arch (e.g. linux-amd64) — note
	// this is the Go GOARCH spelling, unlike the CLI's own linux-x64.
	archive := fmt.Sprintf("%s-v%s-%s-%s.tar.gz", binary, ver, runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("https://github.com/%s/releases/download/plugin-%s-v%s", mirrorRepo, name, ver)

	want, err := mirrorDigest(ctx, base+"/SHA256SUMS", archive)
	if err != nil {
		return err
	}

	pluginDir, err := pulumiPluginDir(name, ver)
	if err != nil {
		return err
	}
	return downloadAndExtractTarGzVerified(ctx, base+"/"+archive, pluginDir, binary, want)
}

// mirrorDigest fetches a mirror release's SHA256SUMS and returns the expected
// digest for one file. A missing entry is an error, not a skip: the whole point is
// that nothing is installed unverified, so "no digest published" must fail loudly
// rather than quietly degrade to the old unverified behaviour.
func mirrorDigest(ctx context.Context, sumsURL, file string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d fetching %s — is this version mirrored? see %s", resp.StatusCode, sumsURL, mirrorRepo)
	}

	// SHA256SUMS is small and fully trusted input from our own release; cap the
	// read anyway so a wrong URL can't stream unbounded into memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == file {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s has no entry for %s — the mirrored release is incomplete for %s/%s",
		sumsURL, file, runtime.GOOS, runtime.GOARCH)
}

func pulumiPluginDir(name, ver string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".pulumi", "plugins", fmt.Sprintf("resource-%s-v%s", name, ver))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return dir, nil
}

func downloadBinary(ctx context.Context, url, dst string, mode os.FileMode) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d fetching %s — asset not found for %s/%s",
			resp.StatusCode, url, runtime.GOOS, runtime.GOARCH)
	}

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 -- dst is an internally generated temp path from the sole caller (selfUpdate), not user input
	if err != nil {
		return err
	}
	n, copyErr := io.CopyN(f, resp.Body, maxPluginBinarySize+1)
	closeErr := f.Close()
	if copyErr != nil && copyErr != io.EOF {
		return copyErr
	}
	if n > maxPluginBinarySize {
		return fmt.Errorf("response body exceeds %d bytes, refusing to write %s", maxPluginBinarySize, dst)
	}
	return closeErr
}

// downloadAndExtractTarGzVerified downloads an archive, checks its SHA-256
// against wantDigest, and only then extracts binaryName from it.
//
// The archive is staged to a temp file and verified BEFORE a single byte is
// extracted. Hashing while streaming straight into the extractor would be less
// code, but it would write an executable to the plugin directory and only
// afterwards discover the bytes were wrong — and that executable is run as part
// of a production deploy. Verify first, then extract.
func downloadAndExtractTarGzVerified(ctx context.Context, url, dir, binaryName, wantDigest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d fetching %s — asset not found for %s/%s (is this version mirrored? see %s)",
			resp.StatusCode, url, runtime.GOOS, runtime.GOARCH, mirrorRepo)
	}

	tmp, err := os.CreateTemp("", "inforge-plugin-*.tar.gz")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	sum := sha256.New()
	n, copyErr := io.CopyN(io.MultiWriter(tmp, sum), resp.Body, maxPluginBinarySize+1)
	if closeErr := tmp.Close(); closeErr != nil && copyErr == nil {
		return closeErr
	}
	if copyErr != nil && copyErr != io.EOF {
		return copyErr
	}
	if n > maxPluginBinarySize {
		return fmt.Errorf("archive %s exceeds %d bytes, refusing to extract", url, maxPluginBinarySize)
	}

	if got := hex.EncodeToString(sum.Sum(nil)); got != wantDigest {
		return fmt.Errorf("checksum mismatch for %s:\n  want %s\n  got  %s\nrefusing to install — the mirrored artifact does not match its published SHA256SUMS",
			url, wantDigest, got)
	}

	f, err := os.Open(tmp.Name()) // #nosec G304 -- tmp.Name() is our own os.CreateTemp path, not external input
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if filepath.Base(hdr.Name) != binaryName {
			continue
		}
		dst := filepath.Join(dir, binaryName)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) // #nosec G304,G302 -- dst is from os.UserHomeDir()+hardcoded plugin dir/binaryName, not external input; binary must be executable and dst is not world-writable
		if err != nil {
			return err
		}
		n, copyErr := io.CopyN(f, tr, maxPluginBinarySize+1)
		closeErr := f.Close()
		if copyErr != nil && copyErr != io.EOF {
			return copyErr
		}
		if n > maxPluginBinarySize {
			return fmt.Errorf("archive entry %q exceeds %d bytes, refusing to extract", hdr.Name, maxPluginBinarySize)
		}
		return closeErr
	}
	return fmt.Errorf("binary %q not found in archive %s", binaryName, url)
}
