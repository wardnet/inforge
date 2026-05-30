package tags

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildTags(t *testing.T) {
	got := BuildTags("use1", "prd", "us-east-1", "bridge", "compute", "bridge")
	assert.Equal(t, TagSet{
		Namespace: "urn:use1:wardnet:prd:bridge:compute:bridge",
		Env:       "prd",
		Region:    "us-east-1",
	}, got)
}

func TestContainerTag(t *testing.T) {
	assert.Equal(t, "urn:use1:wardnet:prd:bridge", ContainerTag("use1", "prd", "bridge"))
}
