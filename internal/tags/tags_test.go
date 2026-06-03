package tags

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContainerTag(t *testing.T) {
	assert.Equal(t, "urn:use1:wardnet:prd:bridge", ContainerTag("use1", "prd", "bridge"))
}

func TestCloudflareTags(t *testing.T) {
	t.Run("with container", func(t *testing.T) {
		got := CloudflareTags("myproject", "prd", "us-east-1", "bridge")
		assert.Equal(t, []string{
			"project:myproject",
			"env:prd",
			"region:us-east-1",
			"container:bridge",
		}, got)
	})

	t.Run("without container", func(t *testing.T) {
		got := CloudflareTags("myproject", "prd", "us-east-1", "")
		assert.Equal(t, []string{
			"project:myproject",
			"env:prd",
			"region:us-east-1",
		}, got)
	})
}

func TestHetznerLabels(t *testing.T) {
	t.Run("with container", func(t *testing.T) {
		got := HetznerLabels("myproject", "prd", "us-east-1", "bridge")
		assert.Equal(t, map[string]string{
			"project":   "myproject",
			"env":       "prd",
			"region":    "us-east-1",
			"container": "bridge",
		}, got)
	})

	t.Run("without container", func(t *testing.T) {
		got := HetznerLabels("myproject", "prd", "us-east-1", "")
		assert.Equal(t, map[string]string{
			"project": "myproject",
			"env":     "prd",
			"region":  "us-east-1",
		}, got)
	})
}
