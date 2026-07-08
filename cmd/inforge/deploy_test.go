package main

import (
	"context"
	"os"
	"strings"
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

	// --ssh-key wins and is returned verbatim (delegated to resolveSSHKey, before any
	// stack access, so a zero Stack is never touched). Cleanup is a non-nil no-op.
	t.Setenv("INFORGE_DEPLOY_KEY", "")
	t.Setenv("INFORGE_DEPLOY_PRIVATE_KEY", "")
	got, cleanup, err := resolveDeployKeyFile(ctx, auto.Stack{}, "/explicit/key")
	if err != nil || got != "/explicit/key" || cleanup == nil {
		t.Fatalf("--ssh-key: got (%q, cleanup!=nil:%v, err:%v), want (/explicit/key,true,nil)", got, cleanup != nil, err)
	}

	// INFORGE_DEPLOY_KEY (a path) is returned directly by the shared resolver.
	t.Setenv("INFORGE_DEPLOY_KEY", "/env/key")
	got, cleanup, err = resolveDeployKeyFile(ctx, auto.Stack{}, "")
	if err != nil || got != "/env/key" || cleanup == nil {
		t.Fatalf("INFORGE_DEPLOY_KEY: got (%q, cleanup!=nil:%v, err:%v), want (/env/key,true,nil)", got, cleanup != nil, err)
	}
	// The stack-config material branch needs a live Stack workspace for GetConfig, so
	// it is exercised by the deploy integration path; TestResolveSSHKey covers the env
	// material path and writeTempKeyFile covers the file write.
}

func TestResolveSSHKey(t *testing.T) {
	// --ssh-key path is returned verbatim.
	t.Setenv("INFORGE_DEPLOY_KEY", "")
	t.Setenv("INFORGE_DEPLOY_PRIVATE_KEY", "")
	if got, cleanup, err := resolveSSHKey("/explicit/key"); err != nil || got != "/explicit/key" || cleanup == nil {
		t.Fatalf("--ssh-key: got (%q, cleanup!=nil:%v, err:%v)", got, cleanup != nil, err)
	}

	// INFORGE_DEPLOY_KEY (a path) beats the material.
	t.Setenv("INFORGE_DEPLOY_KEY", "/env/key")
	t.Setenv("INFORGE_DEPLOY_PRIVATE_KEY", "MATERIAL")
	if got, _, err := resolveSSHKey(""); err != nil || got != "/env/key" {
		t.Fatalf("INFORGE_DEPLOY_KEY: got %q err %v", got, err)
	}

	// Only INFORGE_DEPLOY_PRIVATE_KEY set → materialized to a temp file that cleanup
	// removes. This is the fix: pki renew / releases deploy / db now work from the
	// single deploy-key-material secret the deploy job already sets.
	t.Setenv("INFORGE_DEPLOY_KEY", "")
	t.Setenv("INFORGE_DEPLOY_PRIVATE_KEY", "PRIV-MATERIAL")
	path, cleanup, err := resolveSSHKey("")
	if err != nil {
		t.Fatal(err)
	}
	b, readErr := os.ReadFile(path)
	if readErr != nil || !strings.HasPrefix(string(b), "PRIV-MATERIAL") {
		t.Fatalf("materialized key = %q (read err %v)", string(b), readErr)
	}
	cleanup()
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("cleanup did not remove the temp key: %v", statErr)
	}

	// Nothing set → actionable error.
	t.Setenv("INFORGE_DEPLOY_PRIVATE_KEY", "")
	if _, _, err := resolveSSHKey(""); err == nil {
		t.Error("expected an error when no key source is set")
	}
}
