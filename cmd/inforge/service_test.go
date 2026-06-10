package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRestartCommand(t *testing.T) {
	assert.Equal(t,
		"sudo systemctl restart 'wardnet-ghost.service' && sudo systemctl is-active 'wardnet-ghost.service'",
		restartCommand("wardnet-ghost.service"))
}

// TestRestartCommandQuotesUnit: unit names flow from the deployDescriptor, but
// the remote string must stay shell-safe regardless of what lands there.
func TestRestartCommandQuotesUnit(t *testing.T) {
	cmd := restartCommand("evil; rm -rf /.service")
	assert.NotContains(t, cmd, "restart evil;")
	assert.Contains(t, cmd, "'evil; rm -rf /.service'")
}
