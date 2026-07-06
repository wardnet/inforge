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
			} {
				fmt.Printf("installing pulumi-resource-%s v%s...\n", p.name, p.version)
				if err := installPulumiPlugin(ctx, p.name, p.version, p.repo); err != nil {
					return fmt.Errorf("install %s: %w", p.name, err)
				}
				fmt.Printf("  installed pulumi-resource-%s\n", p.name)
			}

			// Custom providers ship as raw binaries in wardnet/inforge releases.
			// The version must match the running inforge binary; "dev" has no release.
			for _, name := range []string{"neon"} {
				if version == "dev" {
					fmt.Printf("skipping pulumi-resource-%s: build is 'dev' — no GitHub release available\n", name)
					continue
				}
				fmt.Printf("installing pulumi-resource-%s v%s...\n", name, version)
				if err := installCustomPlugin(ctx, name, version); err != nil {
					return fmt.Errorf("install %s: %w", name, err)
				}
				fmt.Printf("  installed pulumi-resource-%s\n", name)
			}

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

// installCustomPlugin downloads a custom inforge provider raw binary from the
// wardnet/inforge GitHub release and installs it into the Pulumi plugins dir.
func installCustomPlugin(ctx context.Context, name, ver string) error {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	binary := "pulumi-resource-" + name

	// goreleaser raw binary name: <binary>_<version>_<os>_<arch>
	filename := fmt.Sprintf("%s_%s_%s_%s", binary, ver, goos, goarch)
	url := releaseAssetURL(ver, filename)

	pluginDir, err := pulumiPluginDir(name, ver)
	if err != nil {
		return err
	}
	return downloadBinary(ctx, url, filepath.Join(pluginDir, binary), 0o755)
}

func pulumiPluginDir(name, ver string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".pulumi", "plugins", fmt.Sprintf("resource-%s-v%s", name, ver))
	if err := os.MkdirAll(dir, 0o755); err != nil {
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

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
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
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, tr)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return fmt.Errorf("binary %q not found in archive %s", binaryName, url)
}
