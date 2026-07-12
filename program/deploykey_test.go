package program

import (
	"strings"
	"testing"
)

func TestResolveDeployKey(t *testing.T) {
	tests := []struct {
		name      string
		fromCfg   string
		fromEnv   string
		want      string
		wantError bool
	}{
		{name: "stack config wins", fromCfg: "cfg-key", fromEnv: "env-key", want: "cfg-key"},
		{name: "env fallback", fromEnv: "env-key", want: "env-key"},
		{name: "neither source", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveDeployKey(tt.fromCfg, tt.fromEnv)
			if tt.wantError {
				if err == nil {
					t.Fatal("want an error when no deploy key is configured, got nil")
				}
				// The message must name both sources, so an operator who hits this
				// on a preview knows exactly which one to set.
				if !strings.Contains(err.Error(), "deploy_private_key") ||
					!strings.Contains(err.Error(), "INFORGE_DEPLOY_PRIVATE_KEY") {
					t.Fatalf("error must name both key sources, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
