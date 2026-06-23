// Package tags builds the URN namespace and label set applied to resources. The
// region slug is supplied by the caller (resolved from the region table) so
// this package stays decoupled from the per-environment region table.
package tags

import "fmt"

// ContainerTag returns the URN identifying a container within an environment.
// Used as the service manifest namespace.
func ContainerTag(slug, env, container string) string {
	return fmt.Sprintf("urn:%s:wardnet:%s:%s", slug, env, container)
}

// Ephemeral carries the ADR-0028 ephemeral-environment labels stamped on every
// cloud resource of an ephemeral env so it is filterable and orphan-auditable.
// The zero value (Enabled=false) adds no labels, so a static env is unchanged.
type Ephemeral struct {
	// Enabled marks the env as ephemeral; when true an "ephemeral":"true" label
	// is applied.
	Enabled bool
	// ExpiresAt is the TTL deadline as epoch seconds (a string, since Hetzner
	// label values forbid the ':' of an RFC3339 timestamp). "" omits the label.
	// It mirrors the stack-config value purely for orphan auditing — the reaper
	// classifies from stack config, never from this label.
	ExpiresAt string
}

// HetznerLabels returns the discrete labels to apply to a Hetzner resource.
// All values conform to Hetzner's label value constraint ([a-zA-Z0-9._-]).
// container may be empty for resources not scoped to a specific container
// (e.g. SSH keys), in which case the container key is omitted. eph adds the
// ephemeral-env labels (ADR-0028); its zero value adds nothing.
func HetznerLabels(project, env, region, container string, eph Ephemeral) map[string]string {
	m := map[string]string{
		"project": project,
		"env":     env,
		"region":  region,
	}
	if container != "" {
		m["container"] = container
	}
	if eph.Enabled {
		m["ephemeral"] = "true"
		if eph.ExpiresAt != "" {
			m["expires_at"] = eph.ExpiresAt
		}
	}
	return m
}

// CloudflareTags returns the tags to apply to a Cloudflare DNS record.
// Tags use the "key:value" string format supported by the Cloudflare API.
// container may be empty for resources not scoped to a specific container,
// in which case the container tag is omitted.
func CloudflareTags(project, env, region, container string) []string {
	t := []string{
		fmt.Sprintf("project:%s", project),
		fmt.Sprintf("env:%s", env),
		fmt.Sprintf("region:%s", region),
	}
	if container != "" {
		t = append(t, fmt.Sprintf("container:%s", container))
	}
	return t
}
