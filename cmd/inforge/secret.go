package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/dbbackup"
	"github.com/wardnet/inforge/internal/grafana"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/otelcol"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/validate"
)

// knownReservedSecrets are the (namespace → KEY) pairs inforge itself consumes
// at deploy from the store's reserved namespace. `secret set --reserved` warns
// when writing outside this set — almost always a typo, since nothing would read
// the value — but does not hard-fail, so the set can grow without a CLI change.
var knownReservedSecrets = map[string]map[string]bool{
	// The reserved "observability" namespace holds the OTLP Basic-auth credential
	// (ADR-0031), the Grafana service-account token, and user-named contact-point
	// secrets like a Grafana OnCall integration URL (ADR-0038). The "*" wildcard lets the
	// latter use arbitrary keys without a spurious "unknown reserved secret" warning.
	otelcol.AuthSecretNamespace: {otelcol.AuthSecretKey: true, grafana.TokenKey: true, "*": true},
	dbbackup.AuthSecretNamespace: {
		dbbackup.AccessKeyIDKey:     true,
		dbbackup.SecretAccessKeyKey: true,
	},
}

// newSecretCmd manages the env's committed encrypted secret store
// (resources/<env>/secrets.enc.yaml, ADR-0017). Every subcommand writes git
// state only: the provider is updated exclusively by `inforge deploy` when the
// store change merges — the CLI deliberately has no provider write path, so a
// deploy from main can never roll back a value the provider already serves.
func newSecretCmd(dir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage the env's git-committed encrypted secret store",
	}
	cmd.AddCommand(
		newSecretInitCmd(dir),
		newSecretSetCmd(dir),
		newSecretLsCmd(dir),
		newSecretRmCmd(dir),
		newSecretRotateCmd(dir),
	)
	return cmd
}

func newSecretInitCmd(dir *string) *cobra.Command {
	var recipient string
	cmd := &cobra.Command{
		Use:           "init <env>",
		Short:         "Create the env's secret store (generates the master key unless --recipient is given)",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runSecretInit(*dir, args[0], recipient)
		},
	}
	cmd.Flags().StringVar(&recipient, "recipient", "", "existing age recipient (age1…) to encrypt to; default generates a fresh key pair")
	return cmd
}

func runSecretInit(dir, env, recipient string) error {
	path := secretstore.Path(dir, env)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists — use `inforge secret rotate %s` to change its recipient", path, env)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return fmt.Errorf("environment directory %s does not exist", filepath.Dir(path))
	}

	var identity string
	if recipient == "" {
		var err error
		identity, recipient, err = secretstore.GenerateIdentity()
		if err != nil {
			return err
		}
	} else if err := secretstore.ParseRecipient(recipient); err != nil {
		return err
	}

	store := &secretstore.Store{Recipient: strings.TrimSpace(recipient)}
	if err := store.Save(path); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "created %s (recipient %s)\n", path, store.Recipient)
	if identity != "" {
		// The identity goes to stdout — and ONLY the identity — so
		// `inforge secret init prd > key.txt` captures nothing else.
		fmt.Fprintf(os.Stderr, "\nthe master identity below is shown ONCE and is not stored anywhere by inforge:\n  - store it as the %s GitHub Actions secret (the deploy decrypts with it)\n  - keep an out-of-band backup; losing it means re-setting every secret\n\n", secretstore.IdentityEnvVar)
		fmt.Println(identity)
	}
	fmt.Fprintf(os.Stderr, "\ncommit %s, then add secrets with `inforge secret set %s <service> <KEY>`\n", path, env)
	return nil
}

func newSecretSetCmd(dir *string) *cobra.Command {
	var generate, reserved bool
	cmd := &cobra.Command{
		Use:           "set <env> <service|namespace> <KEY>",
		Short:         "Encrypt a secret value into the store (stdin, or --generate for a fresh random value)",
		Args:          cobra.ExactArgs(3),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runSecretWrite(*dir, args[0], args[1], args[2], generate, reserved)
		},
	}
	cmd.Flags().BoolVar(&generate, "generate", false, "mint a 32-byte random value instead of reading stdin (the plaintext is never displayed)")
	cmd.Flags().BoolVar(&reserved, "reserved", false, "write an inforge-internal reserved secret: the second arg is a reserved namespace (e.g. observability), not a service")
	return cmd
}

// runSecretWrite is the single value-write path (`set`): encrypt one value to
// the store's recipient and save the store. Set is an upsert — replacing a
// leaked value is the same operation as writing the first one. Git-only by
// design — the new value reaches the provider when the committed store change
// merges and `inforge deploy` runs. With reserved, the value is an
// inforge-internal env-level secret (`name` is a reserved namespace such as
// "observability", not a service) consumed directly by the deploy.
func runSecretWrite(dir, env, name, key string, generate, reserved bool) error {
	// Warn on a probable mistake: a reserved KEY inforge does not consume, or a
	// vault key the service does not reference — both mean the stored value will
	// never be read.
	if reserved {
		// A namespace with the "*" wildcard accepts arbitrary keys (the observability
		// namespace also holds user-named contact-point secrets, ADR-0038), so only
		// warn on an unknown key when the namespace does not opt into wildcards.
		if !knownReservedSecrets[name]["*"] && !knownReservedSecrets[name][key] {
			fmt.Fprintf(os.Stderr, "warning: %s/%s is not a reserved secret inforge consumes — check the namespace and KEY, or nothing will read this value\n", name, key)
		}
	} else {
		svc, err := requireService(dir, env, name)
		if err != nil {
			return err
		}
		if !declaredEncryptedKeys(svc)[key] {
			fmt.Fprintf(os.Stderr, "warning: service %q declares no `vault:%s` secret (service/*.yaml) — the stored value will not be provisioned until it does\n", name, key)
		}
	}

	store, err := secretstore.Load(secretstore.Path(dir, env))
	if err != nil {
		if errors.Is(err, secretstore.ErrNotFound) {
			return fmt.Errorf("%w — run `inforge secret init %s` first", err, env)
		}
		return err
	}

	var value []byte
	if generate {
		// 32 random bytes, base64url without padding: 43 chars, env-var and
		// URL safe. The plaintext exists only in this process.
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generate random value: %w", err)
		}
		value = []byte(base64.RawURLEncoding.EncodeToString(raw))
	} else {
		value, err = readSecretStdin()
		if err != nil {
			return err
		}
	}

	ciphertext, err := secretstore.Encrypt(value, store.Recipient)
	if err != nil {
		return err
	}
	if reserved {
		store.SetReserved(name, key, ciphertext)
	} else {
		store.Set(name, key, ciphertext)
	}
	if err := store.Save(secretstore.Path(dir, env)); err != nil {
		return err
	}

	noun := "service"
	if reserved {
		noun = "reserved namespace"
	}
	fmt.Printf("encrypted %s for %s %q in %s\n", key, noun, name, secretstore.Path(dir, env))
	fmt.Println("\nnext steps:")
	fmt.Println("  1. commit the store file and merge it — the provider is updated by the deploy on merge, never by this CLI")
	if reserved {
		fmt.Println("  2. the value is read by the deploy directly (no service restart needed)")
	} else {
		fmt.Println("  2. after that deploy, restart the service to pick up the new value:")
		fmt.Printf("       inforge service restart %s %s\n", env, name)
	}
	return nil
}

// readSecretStdin reads a secret value from stdin, stripping exactly one
// trailing newline (what `echo`, heredocs and editors append). Interior
// newlines are preserved — multi-line values are legal.
func readSecretStdin() ([]byte, error) {
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintln(os.Stderr, "reading secret value from stdin — paste it and end with Ctrl-D")
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("read value from stdin: %w", err)
	}
	b = []byte(strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"))
	if len(b) == 0 {
		return nil, fmt.Errorf("empty value — pipe the secret on stdin (e.g. `pbpaste | inforge secret set …`)")
	}
	return b, nil
}

func newSecretLsCmd(dir *string) *cobra.Command {
	var reserved bool
	cmd := &cobra.Command{
		Use:           "ls <env> <service|namespace>",
		Short:         "List the secret keys stored for a service",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runSecretLs(*dir, args[0], args[1], reserved)
		},
	}
	cmd.Flags().BoolVar(&reserved, "reserved", false, "list an inforge-internal reserved namespace (e.g. observability) rather than a service")
	return cmd
}

func runSecretLs(dir, env, name string, reserved bool) error {
	store, err := secretstore.Load(secretstore.Path(dir, env))
	if err != nil {
		return err
	}
	if reserved {
		keys := store.ReservedKeys(name)
		if len(keys) == 0 {
			fmt.Printf("no secrets stored for reserved namespace %q\n", name)
			return nil
		}
		for _, k := range keys {
			fmt.Println(k)
		}
		return nil
	}
	svc, err := requireService(dir, env, name)
	if err != nil {
		return err
	}
	keys := store.Keys(name)
	if len(keys) == 0 {
		fmt.Printf("no secrets stored for service %q\n", name)
		return nil
	}
	declared := declaredEncryptedKeys(svc)
	for _, k := range keys {
		if declared[k] {
			fmt.Println(k)
		} else {
			fmt.Printf("%s (service %q declares no `vault:%s` secret in service/*.yaml)\n", k, name, k)
		}
	}
	return nil
}

func newSecretRmCmd(dir *string) *cobra.Command {
	var reserved bool
	cmd := &cobra.Command{
		Use:           "rm <env> <service|namespace> <KEY>",
		Short:         "Remove a secret's ciphertext from the store",
		Args:          cobra.ExactArgs(3),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runSecretRm(*dir, args[0], args[1], args[2], reserved)
		},
	}
	cmd.Flags().BoolVar(&reserved, "reserved", false, "remove from an inforge-internal reserved namespace (e.g. observability) rather than a service")
	return cmd
}

func runSecretRm(dir, env, name, key string, reserved bool) error {
	path := secretstore.Path(dir, env)
	store, err := secretstore.Load(path)
	if err != nil {
		return err
	}
	if reserved {
		if !store.DeleteReserved(name, key) {
			return fmt.Errorf("no secret %s stored for reserved namespace %q", key, name)
		}
		if err := store.Save(path); err != nil {
			return err
		}
		fmt.Printf("removed %s for reserved namespace %q\n", key, name)
		return nil
	}
	if _, err := requireService(dir, env, name); err != nil {
		return err
	}
	if !store.Delete(name, key) {
		return fmt.Errorf("no secret %s stored for service %q", key, name)
	}
	if err := store.Save(path); err != nil {
		return err
	}
	fmt.Printf("removed %s for service %q — also drop any `vault:%s` secret from its service spec (service/*.yaml), or validation will fail\n", key, name, key)
	return nil
}

// newSecretRotateCmd rotates the env's master KEY PAIR (the age identity and
// recipient), not a secret value — values are replaced with `set`, which is an
// upsert. `rekey` is kept as an alias: Vault and the age/SOPS community use
// that word for exactly this operation.
func newSecretRotateCmd(dir *string) *cobra.Command {
	var recipient string
	cmd := &cobra.Command{
		Use:           "rotate <env>",
		Aliases:       []string{"rekey"},
		Short:         "Rotate the env's master key pair and re-encrypt every stored value",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runSecretRotate(*dir, args[0], recipient)
		},
	}
	cmd.Flags().StringVar(&recipient, "recipient", "", "new age recipient (age1…) to re-encrypt to; default generates a fresh key pair")
	return cmd
}

func runSecretRotate(dir, env, recipient string) error {
	path := secretstore.Path(dir, env)
	store, err := secretstore.Load(path)
	if err != nil {
		return err
	}
	// Rotation needs the CURRENT identity: every value is decrypted and
	// re-encrypted, so a lost master key cannot be rotated away from.
	identity, err := secretstore.IdentityFromEnv()
	if err != nil {
		return fmt.Errorf("rotate decrypts every stored value with the current master identity: %w", err)
	}

	var newIdentity string
	if recipient == "" {
		newIdentity, recipient, err = secretstore.GenerateIdentity()
		if err != nil {
			return err
		}
	} else if err := secretstore.ParseRecipient(recipient); err != nil {
		return err
	}

	// Re-encrypt EVERY stored value to the new recipient — both the per-service
	// namespace and the reserved namespace. Missing the reserved map would leave
	// those ciphertexts bound to the old recipient while the store's Recipient
	// header advances, orphaning them (the next deploy's decryptReservedSecret
	// would fail).
	n := 0
	reencrypt := func(kv map[string]map[string]string, set func(ns, key, ct string), label string) error {
		for ns, byKey := range kv {
			for key, ciphertext := range byKey {
				plaintext, err := secretstore.Decrypt(ciphertext, identity)
				if err != nil {
					return fmt.Errorf("decrypt %s %q key %q with the current %s: %w", label, ns, key, secretstore.IdentityEnvVar, err)
				}
				reencrypted, err := secretstore.Encrypt(plaintext, recipient)
				if err != nil {
					return err
				}
				set(ns, key, reencrypted)
				n++
			}
		}
		return nil
	}
	if err := reencrypt(store.Services, store.Set, "service"); err != nil {
		return err
	}
	if err := reencrypt(store.Reserved, store.SetReserved, "reserved namespace"); err != nil {
		return err
	}
	newRecipient := strings.TrimSpace(recipient)

	// A GENERATED identity exists only in this process's memory until it is
	// printed. From the first store write on, material is encrypted to it, so a
	// write failure that returned the error alone would strand ciphertext whose
	// only key was just dropped on the floor (and make the documented "re-run
	// with --recipient <new>" resume impossible — the operator never saw the
	// identity). Any failure past this point therefore prints it.
	failed := func(err error) error {
		if newIdentity == "" {
			return err
		}
		fmt.Fprintf(os.Stderr, "\nrotation FAILED, but a new master identity was already generated and may have\n"+
			"encrypted material in the store files (check `git diff`/`git status`). It is shown ONCE,\n"+
			"below: keep it, then re-run `inforge secret rotate %s --recipient %s` with the OLD\n"+
			"%s to finish the rotation (already-rotated stores are skipped). If no store file\n"+
			"changed, discard it.\n\n", env, newRecipient, secretstore.IdentityEnvVar)
		fmt.Println(newIdentity)
		return err
	}

	// The PKI store's CI-held key material (intermediate keys, root-only root
	// keys) is encrypted to the SAME recipient, so it must be re-keyed in the
	// same operation — otherwise the new master identity cannot decrypt it and
	// every deploy and `inforge pki renew` breaks. The PKI store is written
	// FIRST: if the secret-store write then fails, re-running rotate with the
	// old identity and `--recipient <new>` completes the job (the PKI re-key is
	// a no-op once its recipient already matches).
	pkiCount, err := rekeyPKIStore(dir, env, identity, store.Recipient, newRecipient)
	if err != nil {
		return failed(err)
	}

	store.Recipient = newRecipient
	if err := store.Save(path); err != nil {
		return failed(err)
	}

	fmt.Fprintf(os.Stderr, "re-encrypted %d secret(s) to recipient %s\n", n, store.Recipient)
	if pkiCount > 0 {
		fmt.Fprintf(os.Stderr, "re-encrypted %d CI-held PKI key(s) in %s to the same recipient\n", pkiCount, pki.Path(dir, env))
	}
	if newIdentity != "" {
		fmt.Fprintf(os.Stderr, "\nnew master identity below (shown once) — update the %s GitHub Actions secret before the next deploy:\n\n", secretstore.IdentityEnvVar)
		fmt.Println(newIdentity)
	}
	fmt.Fprintln(os.Stderr, "\ncommit the store file(s); plaintext values are unchanged, so no service restart is needed")

	// A hygiene rotation ends here. A compromise rotation does not: the OLD
	// identity still decrypts the OLD ciphertexts, which live on in git history,
	// so re-encryption cannot un-expose the current values.
	if lines := compromisedValueGuidance(dir, env, store); len(lines) > 0 {
		fmt.Fprintln(os.Stderr, "\nif the OLD identity may have been compromised, re-encryption alone does NOT protect the")
		fmt.Fprintln(os.Stderr, "stored values: old ciphertexts remain decryptable from git history. treat every value")
		fmt.Fprintln(os.Stderr, "below as exposed and replace it (reissue externally-issued credentials at their vendor;")
		fmt.Fprintln(os.Stderr, "use --generate for internally-consumed random secrets), then merge, deploy, and restart:")
		for _, line := range lines {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
	}
	return nil
}

// rekeyPKIStore re-encrypts every CI-held key in the env's PKI store to the new
// master recipient and rewrites the store's `recipient:` header. It is part of
// `secret rotate` and not a separate command on purpose: the PKI store's
// intermediate keys (and a root-only PKI's root key) are encrypted to the very
// recipient rotate replaces, so leaving them behind would silently brick every
// deploy and every `inforge pki renew` — the leaked-key runbook would be the
// thing that breaks the environment.
//
// Only CI-held material moves. A two-tier PKI's cold root (and any root retained
// during an overlap) is encrypted to the store's separate rootRecipient, which a
// master-key rotation does not touch — so the offline INFORGE_PKI_ROOT_KEY is
// never needed here, and no cold key is left undecryptable.
//
// Re-runnable: a store already on the new recipient is a no-op, so a rotate
// interrupted between the two store writes can be completed by re-running it.
func rekeyPKIStore(dir, env, identity, oldRecipient, newRecipient string) (int, error) {
	path := pki.Path(dir, env)
	store, err := pki.Load(path)
	if err != nil {
		if errors.Is(err, pki.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if store.Recipient == newRecipient {
		return 0, nil
	}
	if store.Recipient != oldRecipient {
		return 0, fmt.Errorf("pki store %s is encrypted to recipient %s, not the secret store's %s — rotate cannot re-key it with the current %s; re-key it to %s by hand with the identity that owns it, or the new master key will not decrypt its intermediate keys", path, store.Recipient, oldRecipient, secretstore.IdentityEnvVar, newRecipient)
	}

	rekey := func(ciphertext, what string) (string, error) {
		plaintext, err := secretstore.Decrypt(ciphertext, identity)
		if err != nil {
			return "", fmt.Errorf("decrypt %s in %s with the current %s: %w", what, path, secretstore.IdentityEnvVar, err)
		}
		return secretstore.Encrypt(plaintext, newRecipient)
	}

	n := 0
	for _, name := range store.Names() {
		p, _ := store.Get(name)
		// A root-only PKI's root key is the CI-held issuer key (the encrypt-side
		// rule in Store.RootKeyRecipient); a two-tier root — active or retained in
		// PreviousRoots — is cold, encrypted to the store's separate
		// rootRecipient, and must be left byte-identical. Keying off the topology
		// rather than comparing recipients keeps that true even in the degenerate
		// store where rootRecipient == recipient, where a recipient comparison
		// would re-encrypt the cold root without advancing the rootRecipient
		// header and thereby orphan it.
		if p.Topology == pki.TopologyRootOnly && p.Root.Key != "" {
			ciphertext, err := rekey(p.Root.Key, fmt.Sprintf("PKI %q root key", name))
			if err != nil {
				return 0, err
			}
			p.Root.Key = ciphertext
			n++
		}
		for _, scope := range slices.Sorted(maps.Keys(p.Intermediates)) {
			m := p.Intermediates[scope]
			ciphertext, err := rekey(m.Key, fmt.Sprintf("PKI %q intermediate for scope %q", name, scope))
			if err != nil {
				return 0, err
			}
			m.Key = ciphertext
			p.Intermediates[scope] = m
			n++
		}
		store.Set(name, p)
	}
	store.Recipient = newRecipient
	if err := store.Save(path); err != nil {
		return 0, err
	}
	return n, nil
}

// compromisedValueGuidance returns one ready-to-paste `inforge secret set`
// line per stored (service, KEY). Secrets are service-keyed (ADR-0040), so the
// store's own key IS the CLI handle — no reverse lookup is needed.
//
// An entry naming a service the env does not declare gets a comment instead of a
// command: `secret set` would reject it (requireService), and handing an operator
// a line that errors mid-incident is worse than telling them it needs attention.
// Validation forbids such an orphan, but a compromise rotation is exactly when
// the store is likely to be in a state validation has not yet been run against.
func compromisedValueGuidance(dir, env string, store *secretstore.Store) []string {
	// A resource tree that will not load is not fatal here: fall back to emitting
	// every entry as a plain command rather than losing the whole warning.
	declared := map[string]bool{}
	if specs, err := loadAllServices(dir, env); err == nil {
		for _, svc := range specs {
			declared[svc.Name] = true
		}
	} else {
		for s := range store.Services {
			declared[s] = true
		}
	}

	services := make([]string, 0, len(store.Services))
	for s := range store.Services {
		services = append(services, s)
	}
	sort.Strings(services)
	var lines []string
	for _, service := range services {
		for _, key := range store.Keys(service) {
			if !declared[service] {
				// Not a `secret rm` line: the CLI refuses to address an undeclared
				// service (requireService), so this entry is deleted by hand.
				lines = append(lines, fmt.Sprintf("# %s/%s: service %q is not declared — delete this entry from %s by hand", service, key, service, secretstore.FileName))
				continue
			}
			lines = append(lines, fmt.Sprintf("inforge secret set %s %s %s", env, service, key))
		}
	}
	// Reserved secrets are exposed in git history exactly like service secrets,
	// so a compromise rotation must list them too — with the --reserved reissue
	// form, since they are addressed by namespace, not by service.
	namespaces := make([]string, 0, len(store.Reserved))
	for ns := range store.Reserved {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		for _, key := range store.ReservedKeys(ns) {
			lines = append(lines, fmt.Sprintf("inforge secret set %s %s %s --reserved", env, ns, key))
		}
	}
	return lines
}

// requireService rejects a secret operation against a service the environment
// does not declare — a typo would otherwise write an entry nothing ever reads
// (and, post-ADR-0040, one that fails validation as an orphan). It RETURNS the
// matched spec so the caller can answer "which vault keys does it declare?"
// (declaredEncryptedKeys) without loading and parsing the whole resource tree a
// second time. Both the regional and global slices are searched.
func requireService(dir, env, svcName string) (types.ServiceSpec, error) {
	services, err := loadAllServices(dir, env)
	if err != nil {
		return types.ServiceSpec{}, err
	}
	names := make([]string, 0, len(services))
	for _, svc := range services {
		if svc.Name == svcName {
			return svc, nil
		}
		names = append(names, svc.Name)
	}
	sort.Strings(names)
	return types.ServiceSpec{}, fmt.Errorf("service %q is not declared in %s/%s — known services: %s", svcName, dir, env, strings.Join(names, ", "))
}

// declaredEncryptedKeys returns the vault key names a service declares with
// `vault:KEY`, read off the spec requireService already resolved.
func declaredEncryptedKeys(svc types.ServiceSpec) map[string]bool {
	declared := map[string]bool{}
	for _, src := range svc.Environment {
		if parsed, err := validate.ParseSource(src); err == nil && parsed.Kind == validate.SourceVault {
			declared[parsed.VaultKey] = true
		}
	}
	return declared
}

// loadAllServices merges the regional and global service specs for an env.
func loadAllServices(dir, env string) ([]types.ServiceSpec, error) {
	res, err := loader.LoadResources(env, dir)
	if err != nil {
		return nil, err
	}
	globalRes, err := loader.LoadGlobalResources(env, dir)
	if err != nil {
		return nil, err
	}
	return append(res.Service, globalRes.Service...), nil
}
