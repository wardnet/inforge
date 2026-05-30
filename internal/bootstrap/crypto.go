// Package bootstrap holds the provider-agnostic machinery for first-boot secret
// bootstrapping: minting the age key K and one-time token T, encrypting secret
// manifest fields with SOPS/age, registering K with the external escrow under a
// repo-scoped tenant, and building the bootstrap.yaml a VM reads at first boot.
//
// inforge integrates with, but does not implement, the escrow service (see
// docs/adr/0006). VM creation is stubbed this phase; these helpers are exercised
// by unit tests.
package bootstrap

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"filippo.io/age"
	sops "github.com/getsops/sops/v3"
	"github.com/getsops/sops/v3/aes"
	sopsage "github.com/getsops/sops/v3/age"
	yamlstore "github.com/getsops/sops/v3/stores/yaml"
	"github.com/getsops/sops/v3/version"
)

// Material is the freshly minted bootstrap secret: the age identity K (kept by
// the escrow, redeemed at first boot) and the one-time token T (written into
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

// EncryptYAML SOPS-encrypts a plaintext YAML document to a single age
// recipient. Only values whose key matches encryptedRegex are encrypted; the
// rest stay legible. An empty encryptedRegex encrypts every value.
func EncryptYAML(plaintext []byte, recipient, encryptedRegex string) ([]byte, error) {
	masterKey, err := sopsage.MasterKeyFromRecipient(recipient)
	if err != nil {
		return nil, fmt.Errorf("age master key: %w", err)
	}

	store := &yamlstore.Store{}
	branches, err := store.LoadPlainFile(plaintext)
	if err != nil {
		return nil, fmt.Errorf("load plaintext: %w", err)
	}

	tree := sops.Tree{
		Branches: branches,
		Metadata: sops.Metadata{
			KeyGroups:      []sops.KeyGroup{{masterKey}},
			Version:        version.Version,
			EncryptedRegex: encryptedRegex,
		},
	}

	dataKey, errs := tree.GenerateDataKey()
	if len(errs) > 0 {
		return nil, fmt.Errorf("generate data key: %w", errs[0])
	}

	cipher := aes.NewCipher()
	mac, err := tree.Encrypt(dataKey, cipher)
	if err != nil {
		return nil, fmt.Errorf("encrypt tree: %w", err)
	}
	tree.Metadata.LastModified = time.Now().UTC()
	tree.Metadata.MessageAuthenticationCode, err = cipher.Encrypt(mac, dataKey, tree.Metadata.LastModified.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("encrypt mac: %w", err)
	}

	out, err := store.EmitEncryptedFile(tree)
	if err != nil {
		return nil, fmt.Errorf("emit encrypted file: %w", err)
	}
	return out, nil
}
