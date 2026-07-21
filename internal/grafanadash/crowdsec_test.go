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
	// These names are verified against a real host (crowdsec 1.7.8 + firewall-bouncer
	// 0.0.34) — see the "Host verification" section of ADR-0043. Note the bouncer's
	// series are fw_bouncer_* with no _total suffix, NOT cs_bouncer_*.
	for _, want := range []string{
		"Overview", "Acquisition \\u0026 Detection", "Decisions", "Firewall Bouncer",
		"cs_active_decisions",
		"cs_parser_hits_total",
		"cs_filesource_hits_total",
		"cs_bucket_poured_total",
		"fw_bouncer_banned_ips",
		"fw_bouncer_dropped_packets",
		"fw_bouncer_dropped_bytes",
		"lapi_requests_total",
		`"label":"Edge"`,
	} {
		assert.Contains(t, out, want)
	}

	// Guard the exact mistakes host verification caught: these series do not exist, and a
	// panel querying one silently shows "No data" forever.
	for _, absent := range []string{
		"cs_reader_hits_total",
		"cs_scenario_hits_total",
		"cs_lapi_decisions_total",
		"cs_bouncer_",
	} {
		assert.NotContains(t, out, absent)
	}
}

func TestCrowdsecDeterministic(t *testing.T) {
	a, err := Crowdsec("prd", "u")
	require.NoError(t, err)
	b, err := Crowdsec("prd", "u")
	require.NoError(t, err)
	assert.Equal(t, a, b, "render must be byte-stable")
}
