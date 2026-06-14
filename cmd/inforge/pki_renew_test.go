package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireInfisicalCreds(t *testing.T) {
	providers := map[string]map[string]any{
		"infisical": {
			"clientId":       "cid",
			"clientSecret":   "csecret",
			"siteUrl":        "https://infisical.example",
			"organizationId": "org-1",
		},
	}
	id, secret, site, org, err := requireInfisicalCreds(providers, "global")
	require.NoError(t, err)
	assert.Equal(t, "cid", id)
	assert.Equal(t, "csecret", secret)
	assert.Equal(t, "https://infisical.example", site)
	assert.Equal(t, "org-1", org)

	// Missing provider block names the scope rather than failing later in auth.
	_, _, _, _, err = requireInfisicalCreds(map[string]map[string]any{}, "us-east-1")
	require.ErrorContains(t, err, "us-east-1")
	require.ErrorContains(t, err, "infisical")
}
