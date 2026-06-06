package bootstrapper

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const passwdFixture = `root:x:0:0:root:/root:/bin/bash
# a comment line
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
ghost:x:1001:1002:Ghost Service:/srv/wardnet/ghost:/usr/sbin/nologin
`

func TestParsePasswd(t *testing.T) {
	u, err := parsePasswd(strings.NewReader(passwdFixture), "ghost")
	require.NoError(t, err)
	assert.Equal(t, 1001, u.uid)
	assert.Equal(t, 1002, u.gid)
	assert.Equal(t, "/srv/wardnet/ghost", u.home)
}

func TestParsePasswdNotFound(t *testing.T) {
	_, err := parsePasswd(strings.NewReader(passwdFixture), "nobody")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestParsePasswdSkipsCommentsAndShortLines(t *testing.T) {
	doc := "# only a comment\nbroken:line\n"
	_, err := parsePasswd(strings.NewReader(doc), "broken")
	require.Error(t, err, "a malformed line must not match")
}
