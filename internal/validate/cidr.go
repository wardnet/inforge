package validate

import (
	"fmt"
	"net"
)

// parseCIDR parses a CIDR, returning a clear error on failure.
func parseCIDR(field, cidr string) (*net.IPNet, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid CIDR %q", field, cidr)
	}
	return ipnet, nil
}

// cidrContains reports whether child is wholly contained within parent: the
// parent must contain the child's network address and have a prefix no longer
// than the child's.
func cidrContains(parent, child *net.IPNet) bool {
	if !parent.Contains(child.IP) {
		return false
	}
	parentOnes, _ := parent.Mask.Size()
	childOnes, _ := child.Mask.Size()
	return parentOnes <= childOnes
}
