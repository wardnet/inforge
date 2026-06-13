package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
)

// newPkiCmd manages the env's committed encrypted PKI store
// (resources/<env>/pki.enc.yaml, ADR-0024), the structural twin of the secret
// store. Every subcommand writes git state only: certificate material reaches
// the provider exclusively via `inforge deploy` on merge (later slices), so the
// CLI deliberately has no provider write path.
func newPkiCmd(dir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pki",
		Short: "Manage the env's git-committed encrypted PKI store",
	}
	cmd.AddCommand(
		newPkiInitCmd(dir),
		newPkiAddCmd(dir),
		newPkiIntermediateCmd(dir),
		newPkiLsCmd(dir),
	)
	return cmd
}

func newPkiInitCmd(dir *string) *cobra.Command {
	var recipient, rootRecipient string
	cmd := &cobra.Command{
		Use:           "init <env>",
		Short:         "Create the env's PKI store (generates the offline root recipient unless --root-recipient is given)",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runPkiInit(*dir, args[0], recipient, rootRecipient)
		},
	}
	cmd.Flags().StringVar(&recipient, "recipient", "", "CI age recipient (age1…) for root-only issuer keys; default reuses the env's secret store recipient")
	cmd.Flags().StringVar(&rootRecipient, "root-recipient", "", "existing offline operator recipient (age1…) for cold root keys; default generates a fresh key pair")
	return cmd
}

// runPkiInit creates the store with its two recipients. The CI recipient is, by
// default, the one already recorded in the env's secret store, so a single
// INFORGE_SECRETS_KEY decrypts both stores at deploy — a second CI key is never
// minted silently. The offline root recipient is generated here and its
// identity printed once; it must be kept offline and never reach CI.
func runPkiInit(dir, env, recipient, rootRecipient string) error {
	path := pki.Path(dir, env)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return fmt.Errorf("environment directory %s does not exist", filepath.Dir(path))
	}

	if recipient == "" {
		secStore, err := secretstore.Load(secretstore.Path(dir, env))
		if err != nil {
			if errors.Is(err, secretstore.ErrNotFound) {
				return fmt.Errorf("no CI recipient available: run `inforge secret init %s` first so both stores share %s, or pass --recipient", env, secretstore.IdentityEnvVar)
			}
			return err
		}
		recipient = secStore.Recipient
	} else if err := secretstore.ParseRecipient(recipient); err != nil {
		return err
	}

	var rootIdentity string
	if rootRecipient == "" {
		var err error
		rootIdentity, rootRecipient, err = secretstore.GenerateIdentity()
		if err != nil {
			return err
		}
	} else if err := secretstore.ParseRecipient(rootRecipient); err != nil {
		return err
	}

	store := &pki.Store{
		RootRecipient: strings.TrimSpace(rootRecipient),
		Recipient:     strings.TrimSpace(recipient),
	}
	if err := store.Save(path); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "created %s\n  CI recipient (root-only keys): %s\n  offline root recipient (cold root keys): %s\n", path, store.Recipient, store.RootRecipient)
	if rootIdentity != "" {
		// Only the identity goes to stdout, so `inforge pki init prd > key.txt`
		// captures nothing else.
		fmt.Fprintf(os.Stderr, "\nthe offline root identity below is shown ONCE and is not stored anywhere by inforge:\n  - keep it OFFLINE; it signs two-tier intermediates and must never reach CI\n  - set it as %s when running `inforge pki intermediate`\n\n", pki.RootIdentityEnvVar)
		fmt.Println(rootIdentity)
	}
	fmt.Fprintf(os.Stderr, "\ncommit %s, then add a PKI with `inforge pki add %s <name> --topology two-tier|root-only`\n", path, env)
	return nil
}

func newPkiAddCmd(dir *string) *cobra.Command {
	var topology, scope string
	cmd := &cobra.Command{
		Use:           "add <env> <name>",
		Short:         "Generate a PKI root and record it in the store",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runPkiAdd(*dir, args[0], args[1], topology, scope)
		},
	}
	cmd.Flags().StringVar(&topology, "topology", "", "trust-tree shape: two-tier (cold root + per-scope intermediates, the mesh) or root-only (the daemon)")
	cmd.Flags().StringVar(&scope, "scope", "", "scope a root-only PKI serves (default global; not valid for two-tier)")
	return cmd
}

// runPkiAdd generates a root CA and records it. A two-tier root is cold —
// encrypted to the offline rootRecipient so CI can never sign with it; its
// per-scope intermediates are minted later (#106). A root-only root is
// encrypted to the CI recipient for delivery to an online issuer at deploy.
func runPkiAdd(dir, env, name, topology, scope string) error {
	scope = strings.TrimSpace(scope) // whitespace is never an intended part of a scope
	switch topology {
	case pki.TopologyTwoTier:
		if scope != "" {
			return fmt.Errorf("--scope is not valid for a two-tier PKI; its per-scope intermediates are minted with `inforge pki intermediate`")
		}
	case pki.TopologyRootOnly:
		if scope == "" {
			scope = "global"
		}
	case "":
		return fmt.Errorf("--topology is required (two-tier or root-only)")
	default:
		return fmt.Errorf("unknown topology %q — want two-tier or root-only", topology)
	}

	path := pki.Path(dir, env)
	store, err := pki.Load(path)
	if err != nil {
		if errors.Is(err, pki.ErrNotFound) {
			return fmt.Errorf("%w — run `inforge pki init %s` first", err, env)
		}
		return err
	}
	if _, exists := store.Get(name); exists {
		return fmt.Errorf("PKI %q already exists in %s", name, path)
	}

	certPEM, keyPEM, err := pki.GenerateRoot(name + " root")
	if err != nil {
		return err
	}

	ciphertext, err := secretstore.Encrypt([]byte(keyPEM), store.RootKeyRecipient(topology))
	if err != nil {
		return err
	}

	store.Set(name, pki.PKI{
		Topology: topology,
		Scope:    scope,
		Root:     pki.Material{Cert: certPEM, Key: ciphertext},
	})
	if err := store.Save(path); err != nil {
		return err
	}

	fmt.Printf("created %s PKI %q in %s\n", topology, name, path)
	fmt.Println("\nnext steps:")
	fmt.Println("  1. commit the store file")
	if topology == pki.TopologyTwoTier {
		fmt.Printf("  2. mint a per-scope intermediate for each active scope (global + each region), e.g.:\n       inforge pki intermediate %s %s global\n", env, name)
		fmt.Printf("     intermediate minting needs the offline root identity in %s\n", pki.RootIdentityEnvVar)
	}
	return nil
}

func newPkiIntermediateCmd(dir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "intermediate <env> <name> <scope>",
		Short:         "Mint a per-scope intermediate CA for a two-tier PKI, signed offline by its cold root",
		Args:          cobra.ExactArgs(3),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runPkiIntermediate(*dir, args[0], args[1], args[2])
		},
	}
	return cmd
}

// runPkiIntermediate is the operator-run, offline step that mints a per-scope
// intermediate for a two-tier PKI. It reads the cold root identity from
// INFORGE_PKI_ROOT_KEY (never CI's INFORGE_SECRETS_KEY), decrypts the root key,
// signs the intermediate, and re-encrypts the intermediate key to the CI
// recipient so deploy can mint leaves from it (#108). Scope is recorded as-is
// (trimmed); semantic scope/region validation runs at `inforge validate` (#107).
func runPkiIntermediate(dir, env, name, scope string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return fmt.Errorf("scope is required (e.g. global, or a region)")
	}

	path := pki.Path(dir, env)
	store, err := pki.Load(path)
	if err != nil {
		if errors.Is(err, pki.ErrNotFound) {
			return fmt.Errorf("%w — run `inforge pki init %s` first", err, env)
		}
		return err
	}
	p, ok := store.Get(name)
	if !ok {
		return fmt.Errorf("PKI %q not found in %s", name, path)
	}
	if p.Topology != pki.TopologyTwoTier {
		return fmt.Errorf("PKI %q is %s; only two-tier PKIs have intermediates", name, p.Topology)
	}
	if _, exists := p.Intermediates[scope]; exists {
		return fmt.Errorf("PKI %q already has an intermediate for scope %q (rotation is a separate command)", name, scope)
	}

	rootIdentity := strings.TrimSpace(os.Getenv(pki.RootIdentityEnvVar))
	if rootIdentity == "" {
		return fmt.Errorf("%s is unset — set it to the offline root identity (AGE-SECRET-KEY-…) printed by `inforge pki init`", pki.RootIdentityEnvVar)
	}
	rootKeyPEM, err := secretstore.Decrypt(p.Root.Key, rootIdentity)
	if err != nil {
		return fmt.Errorf("decrypt root key for PKI %q (is %s the right offline root identity?): %w", name, pki.RootIdentityEnvVar, err)
	}
	rootCert, err := pki.ParseCertificate(p.Root.Cert)
	if err != nil {
		return err
	}
	rootSigner, err := pki.ParsePrivateKey(string(rootKeyPEM))
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := pki.GenerateIntermediate(rootCert, rootSigner, fmt.Sprintf("%s %s intermediate", name, scope))
	if err != nil {
		return err
	}
	ciphertext, err := secretstore.Encrypt([]byte(keyPEM), store.IntermediateKeyRecipient())
	if err != nil {
		return err
	}

	if p.Intermediates == nil {
		p.Intermediates = map[string]pki.Material{}
	}
	p.Intermediates[scope] = pki.Material{Cert: certPEM, Key: ciphertext}
	store.Set(name, p)
	if err := store.Save(path); err != nil {
		return err
	}

	fmt.Printf("minted intermediate for PKI %q scope %q in %s\n", name, scope, path)
	fmt.Println("\nnext steps:")
	fmt.Println("  1. commit the store file")
	fmt.Println("  2. deploy mints short-TTL leaves from this intermediate (#108)")
	return nil
}

func newPkiLsCmd(dir *string) *cobra.Command {
	return &cobra.Command{
		Use:           "ls <env>",
		Short:         "List the PKIs in the env's store (topology and tiers present)",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runPkiLs(*dir, args[0])
		},
	}
}

func runPkiLs(dir, env string) error {
	path := pki.Path(dir, env)
	store, err := pki.Load(path)
	if err != nil {
		return err
	}
	names := store.Names()
	if len(names) == 0 {
		fmt.Printf("no PKIs in %s\n", path)
		return nil
	}
	for _, name := range names {
		p, _ := store.Get(name)
		if p.Topology == pki.TopologyRootOnly {
			fmt.Printf("%s\t%s\tscope %s; root\n", name, p.Topology, p.Scope)
			continue
		}
		scopes := make([]string, 0, len(p.Intermediates))
		for s := range p.Intermediates {
			scopes = append(scopes, s)
		}
		sort.Strings(scopes)
		tiers := "(none)"
		if len(scopes) > 0 {
			tiers = strings.Join(scopes, ", ")
		}
		fmt.Printf("%s\t%s\troot; intermediates: %s\n", name, p.Topology, tiers)
	}
	return nil
}
