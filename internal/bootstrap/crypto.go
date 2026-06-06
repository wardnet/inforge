// Package bootstrap holds the provider-agnostic machinery for the key-broker
// first-boot flow: minting the age key K and one-time token T, registering K with
// the external key broker under a repo-scoped tenant, and building the
// bootstrap.yaml a VM reads at first boot.
//
// As of Tier 2 secret values are delivered at runtime by inforge-bootstrap (see
// internal/bootstrapper), so SOPS/age value-baking has been retired and these
// helpers are no longer wired into the deploy path. They are retained for the
// Tier 3 broker-coupling work and exercised by unit tests.
package bootstrap

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"filippo.io/age"
)

// Material is the freshly minted bootstrap secret: the age identity K (kept by
// the key broker, redeemed at first boot) and the one-time token T (written into
// bootstrap.yaml). Recipient is K's public half, used to encrypt secrets.
type Material struct {
	Identity  *age.X25519Identity
	Recipient string
	Token     string
}

// Mint generates a fresh age identity K and a one-time token T.
func Mint() (Material, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return Material{}, fmt.Errorf("generate age identity: %w", err)
	}
	token, err := randomHex(32)
	if err != nil {
		return Material{}, err
	}
	return Material{
		Identity:  id,
		Recipient: id.Recipient().String(),
		Token:     token,
	}, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
