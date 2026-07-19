package grafanadash

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrowdsecRenders(t *testing.T) {
	out, err := Crowdsec("prd", "inforge-prd-crowdsec")
	require.NoError(t, err)
	m := parse(t, out)

	assert.Equal(t, "inforge-prd-crowdsec", m["uid"])
	assert.Equal(t, "CrowdSec Monitoring", m["title"])
	// Every query is env-scoped, and the CrowdSec signals + sections are present.
	assert.Contains(t, out, `deployment_environment_name=\"prd\"`)
	for _, want := range []string{
		"Overview", "Acquisition \\u0026 Detection", "Decisions", "Firewall Bouncer",
		"cs_active_decisions",
		"cs_parser_hits_total",
		"cs_reader_hits_total",
		"cs_lapi_decisions_total",
		"cs_scenario_hits_total",
		"cs_bouncer_dropped_packets_total",
		"cs_bouncer_dropped_bytes_total",
		"cs_bouncer_last_pull",
		`"label":"Edge"`,
	} {
		assert.Contains(t, out, want)
	}
}

func TestCrowdsecDeterministic(t *testing.T) {
	a, err := Crowdsec("prd", "u")
	require.NoError(t, err)
	b, err := Crowdsec("prd", "u")
	require.NoError(t, err)
	assert.Equal(t, a, b, "render must be byte-stable")
}
