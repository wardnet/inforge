// Package aptlock renders the shell inforge uses to make `apt-get update` robust
// against lock contention on a freshly-provisioned host. inforge runs up to five
// apt-installing units on one host in parallel (the ingress and mesh nginx,
// Postgres, the otelcol collector, awscli), and the host's boot-time apt-daily
// timer may also be updating in the background. `-o DPkg::Lock::Timeout` makes
// `apt-get install` wait for the dpkg lock, but on Ubuntu 24.04's apt it does NOT
// cover `apt-get update`'s own lists lock (/var/lib/apt/lists/lock) — so a
// concurrent update fails immediately with "Could not get lock". The fix is to
// retry the update until the lock frees.
package aptlock

// UpdateCmd renders a single shell statement that runs `apt-get update`, retrying
// while the lists lock is held by another apt (a sibling install or the apt-daily
// timer). It keeps DPkg::Lock::Timeout too so a mid-update dpkg-lock wait is also
// tolerated. Non-lock failures (a real repo/network error) still surface: each
// attempt runs fully and the final failure after all retries exits non-zero.
// Safe to embed in a script running under `set -e` / `set -euo pipefail` — the
// per-attempt failure is consumed by the `if` condition.
func UpdateCmd() string {
	return `_apt_updated=; for _i in $(seq 1 12); do ` +
		`if sudo DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 update; then _apt_updated=1; break; fi; ` +
		`echo "inforge: apt-get update blocked (lock held), retry ${_i}/12" >&2; sleep 10; done; ` +
		`[ -n "$_apt_updated" ] || { echo "inforge: apt-get update failed after retries" >&2; exit 1; }`
}
