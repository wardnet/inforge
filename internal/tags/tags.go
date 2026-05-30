// Package tags builds the URN namespace and tag set applied to resources. The
// region slug is supplied by the caller (resolved from the region table) so
// this package stays decoupled from the per-environment region table.
package tags

import "fmt"

// TagSet is the set of tags applied to a resource. namespace is a URN; env and
// region echo the deployment coordinates.
type TagSet struct {
	Namespace string
	Env       string
	Region    string
}

// BuildTags builds the tag set for a resource. slug is the region slug, region
// is the abstract region name.
func BuildTags(slug, env, region, container, resourceType, name string) TagSet {
	return TagSet{
		Namespace: fmt.Sprintf("urn:%s:wardnet:%s:%s:%s:%s", slug, env, container, resourceType, name),
		Env:       env,
		Region:    region,
	}
}

// ContainerTag returns the URN identifying a container within an environment.
func ContainerTag(slug, env, container string) string {
	return fmt.Sprintf("urn:%s:wardnet:%s:%s", slug, env, container)
}
