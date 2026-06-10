// Package secretstore implements the git-native encrypted secret store of
// ADR-0017: app secret values live age-encrypted in the consumer repo at
// resources/<env>/secrets.enc.yaml, keyed by (container, KEY), encrypted to a
// committed X25519 recipient recorded in the store file itself. The `inforge
// secret` CLI is the only writer of the store; `inforge deploy` is the only
// reader that decrypts (with the master identity from INFORGE_SECRETS_KEY) and
// the only writer of the provider — the CLI never touches the provider, so a
// value reaches it exclusively when the store change merges and deploys.
//
// This is native X25519 age, deliberately separate from internal/agehost (the
// SSH-host-key flavour used for the credential delivery contract): the two key
// shapes must never be interchangeable.
package secretstore

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
)

// IdentityEnvVar is the environment variable the deploy (and rekey) read the
// age master identity from. Its value is the AGE-SECRET-KEY-… string printed
// once by `inforge secret init`.
const IdentityEnvVar = "INFORGE_SECRETS_KEY"

// GenerateIdentity mints a fresh X25519 age key pair, returning the private
// identity (AGE-SECRET-KEY-…) and its public recipient (age1…).
func GenerateIdentity() (identity, recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", fmt.Errorf("generate age identity: %w", err)
	}
	return id.String(), id.Recipient().String(), nil
}

// ParseRecipient checks that s is a valid X25519 age recipient (age1…).
func ParseRecipient(s string) error {
	if _, err := age.ParseX25519Recipient(strings.TrimSpace(s)); err != nil {
		return fmt.Errorf("parse age recipient: %w", err)
	}
	return nil
}

// Encrypt age-encrypts plaintext to an X25519 recipient (age1…) and returns
// the ASCII-armored ciphertext — armored so it diffs and reviews as text in
// the committed store. The recipient is trimmed first: it typically arrives
// from a file or flag value carrying a trailing newline.
func Encrypt(plaintext []byte, recipient string) (string, error) {
	r, err := age.ParseX25519Recipient(strings.TrimSpace(recipient))
	if err != nil {
		return "", fmt.Errorf("parse age recipient: %w", err)
	}
	var out bytes.Buffer
	aw := armor.NewWriter(&out)
	w, err := age.Encrypt(aw, r)
	if err != nil {
		return "", fmt.Errorf("init age encryption: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return "", fmt.Errorf("write age plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("finalize age ciphertext: %w", err)
	}
	if err := aw.Close(); err != nil {
		return "", fmt.Errorf("finalize armor: %w", err)
	}
	return out.String(), nil
}

// Decrypt decrypts an ASCII-armored ciphertext with an X25519 identity
// (AGE-SECRET-KEY-…). The plaintext is returned in memory and never written
// to disk.
func Decrypt(armored, identity string) ([]byte, error) {
	id, err := age.ParseX25519Identity(strings.TrimSpace(identity))
	if err != nil {
		return nil, fmt.Errorf("parse age identity: %w", err)
	}
	r, err := age.Decrypt(armor.NewReader(strings.NewReader(strings.TrimSpace(armored))), id)
	if err != nil {
		return nil, fmt.Errorf("decrypt secret: %w", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read decrypted secret: %w", err)
	}
	return plaintext, nil
}

// IdentityFromEnv reads the age master identity from IdentityEnvVar, with an
// actionable error when it is unset.
func IdentityFromEnv() (string, error) {
	id := strings.TrimSpace(os.Getenv(IdentityEnvVar))
	if id == "" {
		return "", fmt.Errorf("%s is unset — set it to the age master identity (AGE-SECRET-KEY-…) printed by `inforge secret init`", IdentityEnvVar)
	}
	return id, nil
}
