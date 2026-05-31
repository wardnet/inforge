package hetzner

import (
	"fmt"

	"github.com/wardnet/inforge/internal/images"
)

var hetznerImages = map[images.CanonicalImage]string{
	images.Ubuntu2404: "ubuntu-24.04",
	images.Ubuntu2604: "ubuntu-26.04",
}

// ResolveImage maps a canonical OS image to the Hetzner image name.
func ResolveImage(img images.CanonicalImage) (string, error) {
	if id, ok := hetznerImages[img]; ok {
		return id, nil
	}
	return "", fmt.Errorf("hetzner has no image for %q", img)
}
