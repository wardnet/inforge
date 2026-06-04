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

func TestValidateResourcesNamingAlias(t *testing.T) {
	err := ValidateResources("naming-alias", testdataDir)
	assert.NoError(t, err, "the naming-alias environment should validate cleanly")
}

func TestValidateResourcesNamingAliasMulti(t *testing.T) {
	err := ValidateResources("naming-alias-multi", testdataDir)
	require.Error(t, err, "the naming-alias-multi environment should fail validation")
	assert.Contains(t, err.Error(), "validation failed")
}
