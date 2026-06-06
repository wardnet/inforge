package bootstrapper

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDropAndExecRefusesPrivilegedID guards the belt-and-suspenders check: if
// passwd resolution ever yields uid/gid 0, dropAndExec must refuse rather than
// exec the service as root. On Linux this hits the explicit guard before any
// syscall; off Linux the stub returns unsupported. Either way it must error and
// must not exec (the bogus path below would fail loudly if it tried).
func TestDropAndExecRefusesPrivilegedID(t *testing.T) {
	err := dropAndExec("/nonexistent/inforge-bootstrap-test", 0, 0, []string{"PATH=/bin"})
	assert.Error(t, err)
}
