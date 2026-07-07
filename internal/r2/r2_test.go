package r2

import (
	"context"
	"strings"
	"testing"
)

func TestNewClientRejectsBadAccountID(t *testing.T) {
	ctx := context.Background()
	cases := map[string]string{
		"empty":       "",
		"uppercase":   "ABCDEF0123456789",
		"trailing ws": "abc123 ",
		"non-hex":     "abcxyz",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewClient(ctx, id); err == nil {
				t.Fatalf("NewClient(%q) = nil error, want validation error", id)
			}
		})
	}
}

func TestNewClientAcceptsHexAccountID(t *testing.T) {
	// A well-formed (lowercase hex) account id passes validation and yields a client;
	// no network is touched by construction.
	c, err := NewClient(context.Background(), "0123456789abcdef")
	if err != nil {
		t.Fatalf("NewClient(valid): %v", err)
	}
	if c == nil {
		t.Fatal("NewClient(valid) returned nil client")
	}
}

func TestNewClientErrorMentionsEnvVar(t *testing.T) {
	_, err := NewClient(context.Background(), "NOPE")
	if err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_ACCOUNT_ID") {
		t.Fatalf("error should name the env var, got %v", err)
	}
}
