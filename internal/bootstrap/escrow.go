package bootstrap

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// EscrowClient registers a minted key K under a one-time token T, scoped to a
// tenant (the repo, owner/repo). The VM later redeems T for K. inforge ships no
// escrow implementation; this interface is satisfied by an external client and
// faked in tests.
type EscrowClient interface {
	// Register stores key (the age identity K) under token T for tenant, so a
	// single redemption of token returns key.
	Register(token, key, tenant string) error
}

// Doc is the bootstrap.yaml written beside an encrypted manifest. A VM reads it
// at first boot to redeem its token with the escrow; once secrets are
// re-encrypted to the host key, the VM deletes it.
type Doc struct {
	EscrowURL string `yaml:"escrow_url"`
	Token     string `yaml:"token"`
	Tenant    string `yaml:"tenant"`
}

// Register registers K with the escrow under tenant and returns the
// bootstrap.yaml document the VM will consume. It does not write the file.
func Register(client EscrowClient, escrowURL, tenant string, m Material) (Doc, error) {
	if err := client.Register(m.Token, m.Identity.String(), tenant); err != nil {
		return Doc{}, fmt.Errorf("register key with escrow: %w", err)
	}
	return Doc{EscrowURL: escrowURL, Token: m.Token, Tenant: tenant}, nil
}

// Marshal renders the bootstrap document as YAML.
func (d Doc) Marshal() ([]byte, error) {
	b, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal bootstrap.yaml: %w", err)
	}
	return b, nil
}
