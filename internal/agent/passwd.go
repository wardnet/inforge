package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// userInfo is a service user's numeric identity and home, resolved from
// /etc/passwd at runtime.
type userInfo struct {
	uid  int
	gid  int
	home string
}

// lookupUser resolves name against the host /etc/passwd.
func lookupUser(name string) (userInfo, error) {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return userInfo{}, fmt.Errorf("open /etc/passwd: %w", err)
	}
	defer func() { _ = f.Close() }()
	return parsePasswd(f, name)
}

// parsePasswd resolves name's uid/gid/home from a passwd-format stream. It is a
// pure parser (no cgo, no os/user) so it is fixture-testable and stays static
// under CGO_ENABLED=0. Lines are name:passwd:uid:gid:gecos:home:shell.
func parsePasswd(r io.Reader, name string) (userInfo, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 7 || f[0] != name {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil {
			return userInfo{}, fmt.Errorf("passwd: bad uid for %q: %w", name, err)
		}
		gid, err := strconv.Atoi(f[3])
		if err != nil {
			return userInfo{}, fmt.Errorf("passwd: bad gid for %q: %w", name, err)
		}
		return userInfo{uid: uid, gid: gid, home: f[5]}, nil
	}
	if err := sc.Err(); err != nil {
		return userInfo{}, fmt.Errorf("read passwd: %w", err)
	}
	return userInfo{}, fmt.Errorf("user %q not found in passwd", name)
}
