package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/otelcol"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/internal/validate"
)

// knownReservedSecrets are the (namespace → KEY) pairs inforge itself consumes
// at deploy from the store's reserved namespace. `secret set --reserved` warns
// when writing outside this set — almost always a typo, since nothing would read
// the value — but does not hard-fail, so the set can grow without a CLI change.
var knownReservedSecrets = map[string]map[string]bool{
	otelcol.AuthSecretNamespace: {otelcol.AuthSecretKey: true},
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
	// Resolve the target namespace and warn on a probable mistake: a reserved KEY
	// inforge does not consume, or a vault key no service references — both mean
	// the stored value will never be read.
	var container string
	var siblings []types.ServiceSpec
	if reserved {
		container = name
		if !knownReservedSecrets[container][key] {
			fmt.Fprintf(os.Stderr, "warning: %s/%s is not a reserved secret inforge consumes — check the namespace and KEY, or nothing will read this value\n", container, key)
		}
	} else {
		var err error
		container, siblings, err = resolveServiceContainer(dir, env, name)
		if err != nil {
			return err
		}
		if declared, err := declaredEncryptedKeys(dir, env, container); err == nil && !declared[key] {
			fmt.Fprintf(os.Stderr, "warning: vault key %s is not referenced by any `vault:%s` secret on a service in container %q (service/*.yaml) — the stored value will not be provisioned until a service declares it\n", key, key, container)
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
		store.SetReserved(container, key, ciphertext)
	} else {
		store.Set(container, key, ciphertext)
	}
	if err := store.Save(secretstore.Path(dir, env)); err != nil {
		return err
	}

	noun := "container"
	if reserved {
		noun = "reserved namespace"
	}
	fmt.Printf("encrypted %s for %s %q in %s\n", key, noun, container, secretstore.Path(dir, env))
	fmt.Println("\nnext steps:")
	fmt.Println("  1. commit the store file and merge it — the provider is updated by the deploy on merge, never by this CLI")
	if reserved {
		fmt.Println("  2. the value is read by the deploy directly (no service restart needed)")
	} else {
		fmt.Println("  2. after that deploy, restart the consuming service(s) to pick up the new value:")
		for _, svc := range siblings {
			fmt.Printf("       inforge service restart %s %s\n", env, svc.Name)
		}
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
		Short:         "List the secret keys stored for a service's container",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runSecretLs(*dir, args[0], args[1], reserved)
		},
	}
	cmd.Flags().BoolVar(&reserved, "reserved", false, "list an inforge-internal reserved namespace (e.g. observability) rather than a service's container")
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
	container, _, err := resolveServiceContainer(dir, env, name)
	if err != nil {
		return err
	}
	keys := store.Keys(container)
	if len(keys) == 0 {
		fmt.Printf("no secrets stored for container %q\n", container)
		return nil
	}
	declared, err := declaredEncryptedKeys(dir, env, container)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if declared[k] {
			fmt.Println(k)
		} else {
			fmt.Printf("%s (not referenced by any `vault:%s` secret on a service in service/*.yaml)\n", k, k)
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
	cmd.Flags().BoolVar(&reserved, "reserved", false, "remove from an inforge-internal reserved namespace (e.g. observability) rather than a service's container")
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
	container, _, err := resolveServiceContainer(dir, env, name)
	if err != nil {
		return err
	}
	if !store.Delete(container, key) {
		return fmt.Errorf("no secret %s stored for container %q", key, container)
	}
	if err := store.Save(path); err != nil {
		return err
	}
	fmt.Printf("removed %s for container %q — also drop any `vault:%s` secret from the container's service specs (service/*.yaml), or validation will fail\n", key, container, key)
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

	// Re-encrypt EVERY stored value to the new recipient — both the service
	// container namespace and the reserved namespace. Missing the reserved map
	// would leave those ciphertexts bound to the old recipient while the store's
	// Recipient header advances, orphaning them (the next deploy's
	// decryptReservedSecret would fail).
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
	if err := reencrypt(store.Containers, store.Set, "container"); err != nil {
		return err
	}
	if err := reencrypt(store.Reserved, store.SetReserved, "reserved namespace"); err != nil {
		return err
	}
	store.Recipient = strings.TrimSpace(recipient)
	if err := store.Save(path); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "re-encrypted %d secret(s) to recipient %s\n", n, store.Recipient)
	if newIdentity != "" {
		fmt.Fprintf(os.Stderr, "\nnew master identity below (shown once) — update the %s GitHub Actions secret before the next deploy:\n\n", secretstore.IdentityEnvVar)
		fmt.Println(newIdentity)
	}
	fmt.Fprintln(os.Stderr, "\ncommit the store file; plaintext values are unchanged, so no service restart is needed")

	// A hygiene rotation ends here. A compromise rotation does not: the OLD
	// identity still decrypts the OLD ciphertexts, which live on in git history,
	// so re-encryption cannot un-expose the current values.
	if lines, err := compromisedValueGuidance(dir, env, store); err == nil && len(lines) > 0 {
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

// compromisedValueGuidance returns one ready-to-paste `inforge secret set`
// line per stored (container, KEY), using the alphabetically-first service of
// each container as the CLI handle (any service sharing the container
// addresses the same entry).
func compromisedValueGuidance(dir, env string, store *secretstore.Store) ([]string, error) {
	services, err := loadAllServices(dir, env)
	if err != nil {
		return nil, err
	}
	handle := map[string]string{}
	for _, svc := range services {
		if cur, ok := handle[svc.Container]; !ok || svc.Name < cur {
			handle[svc.Container] = svc.Name
		}
	}
	containers := make([]string, 0, len(store.Containers))
	for c := range store.Containers {
		containers = append(containers, c)
	}
	sort.Strings(containers)
	var lines []string
	for _, container := range containers {
		for _, key := range store.Keys(container) {
			if svcName, ok := handle[container]; ok {
				lines = append(lines, fmt.Sprintf("inforge secret set %s %s %s", env, svcName, key))
			} else {
				lines = append(lines, fmt.Sprintf("# container %q has no declared service for key %s", container, key))
			}
		}
	}
	// Reserved secrets are exposed in git history exactly like container secrets,
	// so a compromise rotation must list them too — with the --reserved reissue
	// form, since they have no service handle.
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
	return lines, nil
}

// resolveServiceContainer maps a service name to its container — secrets are
// container-scoped, the CLI surface is service-scoped — and returns every
// service sharing that container (they all receive the container's secrets).
// Both the regional and global slices are searched.
func resolveServiceContainer(dir, env, svcName string) (string, []types.ServiceSpec, error) {
	services, err := loadAllServices(dir, env)
	if err != nil {
		return "", nil, err
	}
	var container string
	for _, svc := range services {
		if svc.Name == svcName {
			container = svc.Container
			break
		}
	}
	if container == "" {
		names := make([]string, 0, len(services))
		for _, svc := range services {
			names = append(names, svc.Name)
		}
		sort.Strings(names)
		return "", nil, fmt.Errorf("service %q is not declared in %s/%s — known services: %s", svcName, dir, env, strings.Join(names, ", "))
	}
	var siblings []types.ServiceSpec
	for _, svc := range services {
		if svc.Container == container {
			siblings = append(siblings, svc)
		}
	}
	sort.Slice(siblings, func(i, j int) bool { return siblings[i].Name < siblings[j].Name })
	return container, siblings, nil
}

// declaredEncryptedKeys returns the vault key names declared with `vault:KEY`
// for a container across the env's regional and global service specs.
func declaredEncryptedKeys(dir, env, container string) (map[string]bool, error) {
	declared := map[string]bool{}
	for _, load := range []func(string, string) (types.Resources, error){loader.LoadResources, loader.LoadGlobalResources} {
		res, err := load(env, dir)
		if err != nil {
			return nil, err
		}
		for _, svc := range res.Service {
			if svc.Container != container {
				continue
			}
			for _, src := range svc.Environment {
				if parsed, err := validate.ParseSource(src); err == nil && parsed.Kind == validate.SourceVault {
					declared[parsed.VaultKey] = true
				}
			}
		}
	}
	return declared, nil
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
