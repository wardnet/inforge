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
		got := HetznerLabels("myproject", "prd", "us-east-1", "bridge", Ephemeral{})
		assert.Equal(t, map[string]string{
			"project":   "myproject",
			"env":       "prd",
			"region":    "us-east-1",
			"container": "bridge",
		}, got)
	})

	t.Run("without container", func(t *testing.T) {
		got := HetznerLabels("myproject", "prd", "us-east-1", "", Ephemeral{})
		assert.Equal(t, map[string]string{
			"project": "myproject",
			"env":     "prd",
			"region":  "us-east-1",
		}, got)
	})

	t.Run("ephemeral labels (ADR-0028)", func(t *testing.T) {
		got := HetznerLabels("myproject", "eph-7f3k", "us-east-1", "bridge",
			Ephemeral{Enabled: true, ExpiresAt: "1700000000"})
		assert.Equal(t, map[string]string{
			"project":    "myproject",
			"env":        "eph-7f3k",
			"region":     "us-east-1",
			"container":  "bridge",
			"ephemeral":  "true",
			"expires_at": "1700000000",
		}, got)
	})

	t.Run("ephemeral enabled without expires_at omits the deadline label", func(t *testing.T) {
		got := HetznerLabels("myproject", "eph-7f3k", "us-east-1", "", Ephemeral{Enabled: true})
		assert.Equal(t, map[string]string{
			"project":   "myproject",
			"env":       "eph-7f3k",
			"region":    "us-east-1",
			"ephemeral": "true",
		}, got)
	})
}
