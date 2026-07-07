package main

import (
	"context"
	"os"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

func TestWriteTempKeyFile(t *testing.T) {
	// Material without a trailing newline must gain one (OpenSSH rejects a key file
	// that lacks it), and the file must be 0600.
	path, err := writeTempKeyFile("PRIVATE-KEY-BODY")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "PRIVATE-KEY-BODY\n" {
		t.Errorf("content = %q, want trailing newline added", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}

	// An already-terminated key is left as-is (no double newline).
	path2, err := writeTempKeyFile("BODY\n")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path2) }()
	b2, _ := os.ReadFile(path2)
	if string(b2) != "BODY\n" {
		t.Errorf("content = %q, want no extra newline", string(b2))
	}
}

func TestResolveDeployKeyFilePrecedence(t *testing.T) {
	ctx := context.Background()

	// --ssh-key wins and is returned verbatim with no cleanup — before any stack
	// access, so a zero Stack is never touched.
	t.Setenv("INFORGE_DEPLOY_KEY", "")
	t.Setenv("INFORGE_DEPLOY_PRIVATE_KEY", "")
	got, cleanup, err := resolveDeployKeyFile(ctx, auto.Stack{}, "/explicit/key")
	if err != nil || got != "/explicit/key" || cleanup != nil {
		t.Fatalf("--ssh-key: got (%q, cleanup!=nil:%v, err:%v), want (/explicit/key,false,nil)", got, cleanup != nil, err)
	}

	// INFORGE_DEPLOY_KEY (a path) defers to meshBaseline's own resolveSSHKey: the
	// function returns "" so the env var is read there. No stack access, no cleanup.
	t.Setenv("INFORGE_DEPLOY_KEY", "/env/key")
	got, cleanup, err = resolveDeployKeyFile(ctx, auto.Stack{}, "")
	if err != nil || got != "" || cleanup != nil {
		t.Fatalf("INFORGE_DEPLOY_KEY: got (%q, cleanup!=nil:%v, err:%v), want (\"\",false,nil)", got, cleanup != nil, err)
	}
	// The material fallback (deploy_private_key stack config / INFORGE_DEPLOY_PRIVATE_KEY
	// → temp file) needs a live Stack workspace for GetConfig, so it is exercised by the
	// deploy integration path, not a unit test; writeTempKeyFile covers the file write.
}
