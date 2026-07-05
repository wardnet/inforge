package program

import (
	"errors"
	"fmt"

	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/validate"
)

// encryptedPlaceholder substitutes for encrypted values during a keyless
// preview: previews must stay runnable without any credentials, and the
// resolved value only feeds a provider write that a preview never performs.
const encryptedPlaceholder = "(encrypted)"

// decryptEncryptedSecrets collects every (container, KEY) pair declared with a
// `vault:<KEY>` source across the regional and global service specs, loads the
// environment's committed store (resources/<env>/secrets.enc.yaml), and
// decrypts each value with the INFORGE_SECRETS_KEY master identity. The result
// feeds types.AllOutputs.Encrypted, the map resolveRef serves vault: sources
// from — decryption happens once per deploy, here, provider-neutrally;
// providers only ever see plaintext.
//
// Returns (nil, nil) when no service uses a vault: source, so envs that don't
// opt in never require the store or the key. On a dry run without the key the
// values are placeholders; a real up without the key fails.
func decryptEncryptedSecrets(res, globalRes types.Resources, dir, env string, dryRun bool) (map[string]map[string]string, error) {
	// Container → keys, deduped: several services may share a container, and a
	// key is stored (and decrypted) once per container.
	wanted := map[string]map[string]bool{}
	for _, set := range []types.Resources{res, globalRes} {
		for _, svc := range set.Service {
			for _, src := range svc.Environment {
				parsed, err := validate.ParseSource(src)
				if err != nil || parsed.Kind != validate.SourceVault {
					continue
				}
				if wanted[svc.Container] == nil {
					wanted[svc.Container] = map[string]bool{}
				}
				wanted[svc.Container][parsed.VaultKey] = true
			}
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	storePath := secretstore.Path(dir, env)
	store, err := secretstore.Load(storePath)
	if err != nil {
		if errors.Is(err, secretstore.ErrNotFound) {
			return nil, fmt.Errorf("environment %q declares `vault:` secrets but %s does not exist — run `inforge validate %s` for details", env, storePath, env)
		}
		return nil, err
	}

	identity, err := masterIdentity(dryRun)
	if err != nil {
		return nil, err
	}

	out := make(map[string]map[string]string, len(wanted))
	for container, keys := range wanted {
		out[container] = make(map[string]string, len(keys))
		for key := range keys {
			ciphertext, ok := store.Get(container, key)
			if !ok {
				return nil, fmt.Errorf("no ciphertext for container %q key %q in %s — run `inforge secret set %s <service> %s` and commit the store", container, key, storePath, env, key)
			}
			val, err := decodeValue(ciphertext, identity)
			if err != nil {
				return nil, fmt.Errorf("decrypt secret for container %q key %q: %w", container, key, err)
			}
			out[container][key] = val
		}
	}
	return out, nil
}

// masterIdentity resolves the deploy-side master identity (INFORGE_SECRETS_KEY).
// On a keyless dry run it returns "" — the signal decodeValue turns into the
// placeholder, so previews stay runnable without credentials; on a keyless real
// up it returns the error. Shared by every decrypt path so the keyless/preview
// semantics can never drift between them.
func masterIdentity(dryRun bool) (string, error) {
	identity, err := secretstore.IdentityFromEnv()
	if err != nil {
		if dryRun {
			return "", nil
		}
		return "", err
	}
	return identity, nil
}

// decodeValue decrypts one armored ciphertext with an already-resolved identity;
// an empty identity (keyless preview) yields the placeholder, since a preview
// never provisions the value.
func decodeValue(ciphertext, identity string) (string, error) {
	if identity == "" {
		return encryptedPlaceholder, nil
	}
	plaintext, err := secretstore.Decrypt(ciphertext, identity)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// decryptReservedSecret decrypts one env-level RESERVED secret (a value outside
// the service container namespace — e.g. the observability OTLP credential,
// ADR-0031) from the store. It is deliberately independent of the `vault:`
// wanted-set: reserved secrets are referenced by no service, so routing them
// through decryptEncryptedSecrets (which returns nil when no service uses
// vault:) would never surface them. Returns "" when the store or the entry is
// absent — the caller decides whether that is a misconfiguration — and shares
// the identity/preview handling with the container path via masterIdentity /
// decodeValue.
func decryptReservedSecret(dir, env, namespace, key string, dryRun bool) (string, error) {
	store, err := secretstore.Load(secretstore.Path(dir, env))
	if err != nil {
		if errors.Is(err, secretstore.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	ciphertext, ok := store.GetReserved(namespace, key)
	if !ok {
		return "", nil
	}
	identity, err := masterIdentity(dryRun)
	if err != nil {
		return "", err
	}
	val, err := decodeValue(ciphertext, identity)
	if err != nil {
		return "", fmt.Errorf("decrypt reserved secret %s/%s: %w", namespace, key, err)
	}
	return val, nil
}
