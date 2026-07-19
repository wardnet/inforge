package grafanaalert

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrowdsecBuiltIns(t *testing.T) {
	as := CrowdsecBuiltIns("prd")
	require.Len(t, as, 2)

	by := map[string]Alert{}
	for _, a := range as {
		by[a.Rule.Name] = a
	}
	require.Contains(t, by, "CrowdSec Acquisition Stalled")
	require.Contains(t, by, "CrowdSec Bouncer Not Pulling")

	for _, a := range as {
		// Warnings, env-scoped, and NoDataOK — absence (an unverified metric name, or
		// CrowdSec not yet scraped) must not fire; they alert only on a present-but-bad signal.
		assert.Equal(t, SeverityWarning, a.Severity, a.Rule.Name)
		assert.Equal(t, NoDataOK, a.Rule.NoDataState, a.Rule.Name)
		require.NotEmpty(t, a.Rule.Data)
		assert.Contains(t, a.Rule.Data[0].Model, `deployment_environment_name=\"prd\"`, a.Rule.Name)
	}

	// The acquisition alert fires on a low parse rate; the bouncer alert on a stale pull.
	assert.Contains(t, by["CrowdSec Acquisition Stalled"].Rule.Data[0].Model, "cs_parser_hits_total")
	assert.Contains(t, by["CrowdSec Bouncer Not Pulling"].Rule.Data[0].Model, "cs_bouncer_last_pull")
	assert.True(t, strings.HasPrefix(by["CrowdSec Acquisition Stalled"].Rule.Data[0].Model, "{"), "model is JSON")
}
