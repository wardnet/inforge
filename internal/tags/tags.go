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

// HetznerLabels returns the discrete labels to apply to a Hetzner resource.
// All values conform to Hetzner's label value constraint ([a-zA-Z0-9._-]).
// container may be empty for resources not scoped to a specific container
// (e.g. SSH keys), in which case the container key is omitted.
func HetznerLabels(project, env, region, container string) map[string]string {
	m := map[string]string{
		"project": project,
		"env":     env,
		"region":  region,
	}
	if container != "" {
		m["container"] = container
	}
	return m
}
