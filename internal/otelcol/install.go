package otelcol

import (
	"fmt"
	"strings"
)

// InstallScript renders the idempotent shell that installs the off-the-shelf
// otelcol-contrib .deb at the given version onto a host (ADR-0031). It mirrors the
// inforge-bootstrap download step: detect the host arch, pin the version, download
// the .deb + release checksums, verify the sha256, then apt-install the local .deb
// (apt runs the package's postinstall, creating the otelcol-contrib user and the
// systemd unit). It is a no-op when the pinned version is already installed.
//
// The install does NOT write the collector config or enable the unit — that is the
// caller's apply step, which depends on this one. apt is told to keep our config on
// a package upgrade (--force-confold) so a later .deb bump never reverts the config
// inforge wrote.
//
// version carries no leading "v" (e.g. "0.155.0"); it is single-quoted into a shell
// var so it is injection-safe while composing with the host-side ${arch} expansion.
func InstallScript(version string) string {
	base := DownloadBaseURL(version)
	return strings.Join([]string{
		"set -e",
		"ver=" + shQuote(version),
		// Skip entirely when this exact version is already installed.
		`if [ "$(dpkg-query -W -f='${Version}' ` + Package + ` 2>/dev/null || true)" = "$ver" ]; then`,
		fmt.Sprintf(`  echo "%s $ver already installed"; exit 0`, Package),
		"fi",
		"arch=$(uname -m)",
		"case \"$arch\" in",
		"  x86_64) arch=amd64 ;;",
		"  aarch64) arch=arm64 ;;",
		"  *) echo \"unsupported host arch: $arch\" >&2; exit 1 ;;",
		"esac",
		fmt.Sprintf(`asset="%s_${ver}_linux_${arch}.deb"`, Package),
		fmt.Sprintf("base=%s", shQuote(base)),
		fmt.Sprintf("sums_name=%s", shQuote(ChecksumsAsset)),
		"tmp=$(mktemp)",
		"sums=$(mktemp)",
		`trap 'rm -f "$tmp" "$sums"' EXIT`,
		`curl -fsSL "${base}/${asset}" -o "$tmp"`,
		`curl -fsSL "${base}/${sums_name}" -o "$sums"`,
		// Pull the expected sha256 for exactly this asset; an absent line is fatal.
		`want=$(awk -v f="$asset" '$2==f {print $1}' "$sums")`,
		`[ -n "$want" ] || { echo "no checksum for $asset in release" >&2; exit 1; }`,
		`got=$(sha256sum "$tmp" | awk '{print $1}')`,
		`[ "$want" = "$got" ] || { echo "checksum mismatch for $asset" >&2; exit 1; }`,
		// Install the local .deb. --force-confold keeps the config file inforge owns
		// across a package upgrade; noninteractive avoids any conffile prompt.
		`sudo DEBIAN_FRONTEND=noninteractive apt-get install -y ` +
			`-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$tmp"`,
	}, "\n")
}

// ApplyScript writes the rendered config (0644, root) and reloads the collector. The
// credential file is written separately by the caller (it carries a secret and is
// chown'd to the collector user); this enables the unit and restarts it so a changed
// config or credential takes effect. Restart (not reload) is used because a changed
// ${file:…} credential is only re-read on start.
func ApplyScript(config string) string {
	return strings.Join([]string{
		"set -e",
		WriteFileScript(ConfigPath, config, "0644", "root:root"),
		fmt.Sprintf("sudo systemctl enable %s", shQuote(Service)),
		fmt.Sprintf("sudo systemctl restart %s", shQuote(Service)),
	}, "\n")
}

// CredentialScript writes the base64 OTLP Basic-auth value to CredentialPath as a
// 0600 file owned by the collector user, so only the collector can read it. The
// value is supplied already base64-encoded by the caller.
func CredentialScript(authB64 string) string {
	return WriteFileScript(CredentialPath, authB64, "0600", User+":"+User)
}

// WriteFileScript renders sudo shell that writes content to absPath with the given
// mode and owner, creating the parent dir. content is base64-encoded for transport
// so arbitrary bytes (incl. a secret) survive intact and never appear in argv as
// plaintext. The decode is piped straight into a root-owned install.
func WriteFileScript(absPath, content, mode, owner string) string {
	enc := base64Encode(content)
	dir := parentDir(absPath)
	return strings.Join([]string{
		fmt.Sprintf("sudo install -d -m 0755 %s", shQuote(dir)),
		fmt.Sprintf(`printf %%s %s | base64 -d | sudo install -m %s /dev/stdin %s`, shQuote(enc), shQuote(mode), shQuote(absPath)),
		fmt.Sprintf("sudo chown %s %s", shQuote(owner), shQuote(absPath)),
	}, "\n")
}
