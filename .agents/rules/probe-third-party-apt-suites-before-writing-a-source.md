# Probe a third-party apt suite before writing its sources file

When adding a third-party apt repository to a host, never interpolate the host's
`${VERSION_CODENAME}` into the sources file and hope the vendor publishes it. Probe the
suite's `Release` object first, fall back to the newest suite the vendor is known to
publish for that distro, and **fail with the sources file absent** if none resolves.
Remove any sources file from a previous run *before* the first `apt-get` call.

`internal/crowdsec.InstallScript` + `suiteResolution` + `FallbackSuites` are the reference
shape. `internal/nginx` and `internal/postgres` still interpolate `${VERSION_CODENAME}`
directly; both vendors happen to publish for our target today, but they carry the same
latent failure and should adopt this shape when either lags a distro release.

## Applies to

Any renderer that writes `/etc/apt/sources.list.d/*.list`: `internal/crowdsec/install.go`,
`internal/nginx/install.go`, `internal/postgres/install.go`.

## Why

`apt-get update` fails hard on an unreachable source, and inforge's installers wrap it in
a retry-then-exit-1 loop. So a sources file pointing at a suite that 404s does not just
fail its own install — it makes **every later apt-using provisioning step on that host**
(nginx, otelcol, postgres) fail too, indefinitely, long after the feature that wrote it
was disabled. The blast radius of guessing wrong is the whole host, not the one package.

This is not hypothetical. CrowdSec's packagecloud repo publishes nothing for Ubuntu 26.04
(`resolute` 404s; the newest is `noble`), so enabling the edge security tier wrote a
broken source onto both production edge hosts and left them needing a manual `rm`.
Vendors routinely lag a distro release by months, and their packages are usually
suite-generic — so probing costs one HTTP request and an older suite is a correct answer.
