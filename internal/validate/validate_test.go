package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testdataDir = "testdata"

func TestValidateResourcesOK(t *testing.T) {
	err := ValidateResources("ok", testdataDir)
	assert.NoError(t, err, "the ok environment should validate cleanly")
}

func TestValidateResourcesBad(t *testing.T) {
	err := ValidateResources("bad", testdataDir)
	require.Error(t, err, "the bad environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}
