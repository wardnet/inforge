package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInfisicalCreds(t *testing.T) {
	providers := map[string]map[string]any{
		"infisical": {
			"clientId":       "cid",
			"clientSecret":   "csecret",
			"siteUrl":        "https://infisical.example",
			"organizationId": "org-1",
		},
	}
	id, secret, site, org := infisicalCreds(providers)
	assert.Equal(t, "cid", id)
	assert.Equal(t, "csecret", secret)
	assert.Equal(t, "https://infisical.example", site)
	assert.Equal(t, "org-1", org)

	// Missing provider block yields empty strings, not a panic.
	id, secret, site, org = infisicalCreds(map[string]map[string]any{})
	assert.Empty(t, id+secret+site+org)
}
