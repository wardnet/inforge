package bootstrap

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// KeyBrokerClient registers a minted key K under a one-time token T, scoped to a
// tenant (the repo, owner/repo). The VM later redeems T for K. inforge ships no
// key broker implementation; this interface is satisfied by an external client and
// faked in tests.
type KeyBrokerClient interface {
	// Register stores key (the age identity K) under token T for tenant with
	// the given TTL. After ttlSeconds the key broker drops the entry; a single
	// redemption before expiry also removes it.
	Register(token, key, tenant string, ttlSeconds int) error
}

// Doc is the bootstrap.yaml written beside an encrypted manifest. A VM reads it
// at first boot to redeem its token with the key broker; once secrets are
// re-encrypted to the host key, the VM deletes it.
type Doc struct {
	BrokerURL string `yaml:"broker_url"`
	Token     string `yaml:"token"`
	Tenant    string `yaml:"tenant"`
}

// Register registers K with the key broker under tenant and returns the
// bootstrap.yaml document the VM will consume. It does not write the file.
func Register(client KeyBrokerClient, brokerURL, tenant string, m Material, ttlSeconds int) (Doc, error) {
	if err := client.Register(m.Token, m.Identity.String(), tenant, ttlSeconds); err != nil {
		return Doc{}, fmt.Errorf("register key with key broker: %w", err)
	}
	return Doc{BrokerURL: brokerURL, Token: m.Token, Tenant: tenant}, nil
}

// Marshal renders the bootstrap document as YAML.
func (d Doc) Marshal() ([]byte, error) {
	b, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("marshal bootstrap.yaml: %w", err)
	}
	return b, nil
}
