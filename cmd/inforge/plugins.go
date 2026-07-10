package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

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

			// Standard Pulumi providers: download tar.gz from their GitHub releases.
			// Versions are pinned to match the SDK modules in go.mod.
			type stdPlugin struct{ name, version, repo string }
			for _, p := range []stdPlugin{
				{"hcloud", "1.38.0", "pulumi/pulumi-hcloud"},
				{"cloudflare", "6.17.0", "pulumi/pulumi-cloudflare"},
				// pulumi-random backs stable per-service database passwords (ADR-0036).
				{"random", "4.16.8", "pulumi/pulumi-random"},
				// pulumiverse/grafana pushes dashboards + alerts (ADR-0038). Note the
				// pulumiverse org publishes the same asset layout as pulumi/*.
				{"grafana", "1.0.0", "pulumiverse/pulumi-grafana"},
			} {
				fmt.Printf("installing pulumi-resource-%s v%s...\n", p.name, p.version)
				if err := installPulumiPlugin(ctx, p.name, p.version, p.repo); err != nil {
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

// installPulumiPlugin downloads a published Pulumi provider archive from GitHub
// and extracts the binary into the Pulumi plugins directory.
func installPulumiPlugin(ctx context.Context, name, ver, repo string) error {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	binary := "pulumi-resource-" + name

	// Pulumi provider archives use hyphen-separated os-arch (e.g. linux-amd64).
	archive := fmt.Sprintf("%s-v%s-%s-%s.tar.gz", binary, ver, goos, goarch)
	url := fmt.Sprintf("https://github.com/%s/releases/download/v%s/%s", repo, ver, archive)

	pluginDir, err := pulumiPluginDir(name, ver)
	if err != nil {
		return err
	}
	return downloadAndExtractTarGz(ctx, url, pluginDir, binary)
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

func downloadAndExtractTarGz(ctx context.Context, url, dir, binaryName string) error {
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

	gz, err := gzip.NewReader(resp.Body)
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
