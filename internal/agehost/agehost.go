// Package agehost is the single host-key age implementation shared by both sides
// of the credential delivery contract: inforge (the program) encrypts a
// per-service credential to a host's SSH host key with Encrypt; inforge-agent
// decrypts it on that host with Decrypt. Keeping one implementation here — and
// having both callers and the interop test exercise these exact functions —
// guarantees the producer and consumer can never drift.
//
// It is plain age (not SOPS): the credential blob is opaque, so there is nothing
// to selectively encrypt. The package is deliberately age-only and Pulumi-free so
// the dependency-light agent can import it.
package agehost

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// Encrypt age-encrypts plaintext to an SSH host public key in authorized_keys
// form (the contents of an ssh_host_ed25519_key.pub, e.g. "ssh-ed25519 AAAA...
// comment"). Only the matching host private key can decrypt the result, so no
// extra key material is provisioned. The public key is trimmed first: a .pub
// file (and the `cat`/Stdout that reads it) carries a trailing newline that
// agessh.ParseRecipient would reject.
func Encrypt(plaintext []byte, sshPublicKey string) ([]byte, error) {
	recipient, err := agessh.ParseRecipient(strings.TrimSpace(sshPublicKey))
	if err != nil {
		return nil, fmt.Errorf("parse host public key as age recipient: %w", err)
	}
	var out bytes.Buffer
	w, err := age.Encrypt(&out, recipient)
	if err != nil {
		return nil, fmt.Errorf("init age encryption: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("write age plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("finalize age ciphertext: %w", err)
	}
	return out.Bytes(), nil
}

// Decrypt age-decrypts ciphertext in memory using an SSH host private key in
// OpenSSH PEM form (the contents of /etc/ssh/ssh_host_ed25519_key). The
// plaintext is returned to the caller and never written to disk.
func Decrypt(ciphertext, hostKeyPEM []byte) ([]byte, error) {
	identity, err := agessh.ParseIdentity(hostKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse host key as age identity: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	plaintext, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read decrypted credential: %w", err)
	}
	return plaintext, nil
}
