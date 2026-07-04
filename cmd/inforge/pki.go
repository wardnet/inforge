package main

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/meshcert"
	"github.com/wardnet/inforge/internal/meshpaths"
	"github.com/wardnet/inforge/internal/meshplan"
	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/internal/pki"
	"github.com/wardnet/inforge/internal/secretstore"
	"github.com/wardnet/inforge/internal/types"
	"github.com/wardnet/inforge/providers/infisical"
)

// newPkiCmd manages the env's committed encrypted PKI store
// (resources/<env>/pki.enc.yaml, ADR-0024), the structural twin of the secret
// store. The store subcommands (init/add/intermediate/ls) write git state only;
// `renew` is the one provider-writing subcommand — it mints fresh mesh leaves
// from the committed intermediates and writes them to the secrets provider,
// deliberately decoupled from `inforge deploy` so cert renewal never pushes
// un-shipped infra changes.
func newPkiCmd(dir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pki",
		Short: "Manage the env's git-committed encrypted PKI store",
	}
	cmd.AddCommand(
		newPkiInitCmd(dir),
		newPkiAddCmd(dir),
		newPkiIntermediateCmd(dir),
		newPkiRotateCmd(dir),
		newPkiRecoverIntermediateCmd(dir),
		newPkiRenewCmd(dir),
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
		return fmt.Errorf("PKI %q already has an intermediate for scope %q — rotate it with `inforge pki rotate %s %s --intermediate %s`", name, scope, env, name, scope)
	}
	// Mid root-overlap the cold root is the NEW root, so a first-mint here would
	// sign the scope under the new root only — invisible to consumers still on
	// the old root, with no PreviousIntermediates counterpart. Refuse it for the
	// same reason re-mint is refused; the operator finalizes the rotation first.
	if len(p.PreviousRoots) > 0 {
		return fmt.Errorf("PKI %q is in a root overlap — finalize it (`inforge pki rotate %s %s --root --finalize`) before minting a new scope intermediate", name, env, name)
	}

	mat, err := mintScopeIntermediate(store, p, name, scope)
	if err != nil {
		return err
	}
	if p.Intermediates == nil {
		p.Intermediates = map[string]pki.Material{}
	}
	p.Intermediates[scope] = mat
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

func newPkiRotateCmd(dir *string) *cobra.Command {
	var leaf, root, finalize bool
	var intermediate string
	cmd := &cobra.Command{
		Use:   "rotate <env> <name>",
		Short: "Rotate a tier of a two-tier PKI: --leaf, --intermediate <scope>, or --root",
		Long: "Rotate one tier of a two-tier PKI. Exactly one of --leaf, --intermediate, or --root.\n\n" +
			"  --leaf                 short-TTL leaves rotate by redeploy; this prints how (see `inforge pki renew`).\n" +
			"  --intermediate <scope> re-mint one scope's intermediate from the cold root (offline; needs " + pki.RootIdentityEnvVar + ").\n" +
			"                         Invisible to other regions — the mesh's regional boundary keeps each region's\n" +
			"                         intermediate out of every other region's trust bundle.\n" +
			"  --root                 dual-root overlap: mint a new cold root, retain the old one, and re-sign every\n" +
			"                         intermediate from the new root (keys preserved, so live leaves keep verifying).\n" +
			"                         Both roots verify during the overlap; run again with --finalize to drop the old root.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			modes := 0
			for _, on := range []bool{leaf, intermediate != "", root} {
				if on {
					modes++
				}
			}
			if modes != 1 {
				return fmt.Errorf("choose exactly one of --leaf, --intermediate <scope>, or --root")
			}
			if finalize && !root {
				return fmt.Errorf("--finalize is only valid with --root")
			}
			switch {
			case leaf:
				return runPkiRotateLeaf(args[0], args[1])
			case intermediate != "":
				return runPkiRotateIntermediate(*dir, args[0], args[1], intermediate)
			default:
				return runPkiRotateRoot(*dir, args[0], args[1], finalize)
			}
		},
	}
	cmd.Flags().BoolVar(&leaf, "leaf", false, "explain how short-TTL leaves rotate (via `inforge pki renew`)")
	cmd.Flags().StringVar(&intermediate, "intermediate", "", "re-mint the named scope's intermediate from the cold root")
	cmd.Flags().BoolVar(&root, "root", false, "rotate the cold root with a dual-root overlap")
	cmd.Flags().BoolVar(&finalize, "finalize", false, "with --root: drop the retained old root, ending the overlap")
	return cmd
}

// runPkiRotateLeaf documents leaf rotation. Mesh leaves are short-TTL and expiry
// is the only revocation mechanism (ADR-0024), so there is nothing to mint here:
// a fresh leaf is produced by `inforge pki renew` (all services) or by `inforge
// releases deploy` (the released service), and each host re-projects it on its
// renewal timer. Rotating a single PKI's leaves is just running renew.
func runPkiRotateLeaf(env, name string) error {
	fmt.Printf("Leaf certificates for PKI %q rotate by re-minting, not by an in-place command.\n\n", name)
	fmt.Println("Mesh leaves are short-TTL (90 days); expiry IS revocation — there is no CRL/OCSP.")
	fmt.Println("To roll every service's leaf now:")
	fmt.Printf("    export %s=\"AGE-SECRET-KEY-…\"   # the CI master identity\n", secretstore.IdentityEnvVar)
	fmt.Printf("    inforge pki renew %s\n\n", env)
	fmt.Println("Each host re-projects the new leaf on its `wardnet-<svc>-renew.timer` (daily); to apply")
	fmt.Println("immediately, run `systemctl start wardnet-<svc>-renew.service` on the host. A newly")
	fmt.Println("released service also gets a fresh leaf from `inforge releases deploy` before it restarts.")
	return nil
}

// runPkiRotateIntermediate re-mints one scope's intermediate from the cold root.
// It is a planned, graceful rotation: a fresh intermediate key replaces the old,
// signed offline by the cold root. Because the mesh's regional boundary keeps a
// region's intermediate out of every other region's trust bundle, this is
// invisible outside the rotated scope; within the scope, a `inforge pki renew`
// re-mints the leaves (and bundle) from the new intermediate.
func runPkiRotateIntermediate(dir, env, name, scope string) error {
	if err := reissueIntermediate(dir, env, name, scope); err != nil {
		return err
	}
	fmt.Printf("rotated intermediate for PKI %q scope %q (fresh key, signed by the cold root)\n", name, scope)
	fmt.Println("\nnext steps:")
	fmt.Println("  1. commit the store file")
	fmt.Printf("  2. re-mint leaves from the new intermediate within the leaf TTL: inforge pki renew %s\n", env)
	fmt.Println("     other regions are unaffected — the regional boundary isolates this scope")
	return nil
}

func newPkiRecoverIntermediateCmd(dir *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "recover-intermediate <env> <name> <scope>",
		Short:         "Compromise recovery: re-mint one scope's intermediate from the cold root and force re-projection",
		Args:          cobra.ExactArgs(3),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return runPkiRecoverIntermediate(*dir, args[0], args[1], args[2])
		},
	}
	return cmd
}

// runPkiRecoverIntermediate handles a *compromised* intermediate key. The crypto
// is the same fresh-key re-mint as a planned rotation, but the operational
// posture is not: the old key is burned, so there is no overlap — every leaf it
// signed must be replaced and re-projected to hosts immediately, rather than
// drifting in on the daily timer. The guidance reflects that urgency. Re-minting
// is offline (cold root); the follow-up renew is the online CI step.
func runPkiRecoverIntermediate(dir, env, name, scope string) error {
	if err := reissueIntermediate(dir, env, name, scope); err != nil {
		return err
	}
	fmt.Printf("re-minted intermediate for PKI %q scope %q after compromise (fresh key, signed by the cold root)\n", name, scope)
	fmt.Println("\nthe old intermediate key is now BURNED — every leaf it signed is untrusted. Act now:")
	fmt.Println("  1. commit the store file")
	fmt.Printf("  2. re-mint leaves from the new intermediate IMMEDIATELY (CI identity): inforge pki renew %s\n", env)
	fmt.Println("  3. force every host in this scope to re-project NOW — do not wait for the daily timer:")
	fmt.Println("       systemctl start wardnet-<svc>-renew.service   # on each host running an affected service")
	fmt.Println("  4. confirm no service still presents a leaf signed by the old key")
	return nil
}

// reissueIntermediate re-mints an *existing* scope intermediate of a two-tier PKI
// with a fresh key, signed offline by the cold root, and re-encrypts the new key
// to the CI recipient. It mirrors runPkiIntermediate's custody flow but replaces
// in place (the scope must already have an intermediate — minting a brand-new
// scope is `inforge pki intermediate`). Shared by the planned rotate path and the
// compromise-recovery path, which differ only in their next-step messaging.
func reissueIntermediate(dir, env, name, scope string) error {
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
	if _, exists := p.Intermediates[scope]; !exists {
		return fmt.Errorf("PKI %q has no intermediate for scope %q to rotate — mint one with `inforge pki intermediate %s %s %s`", name, scope, env, name, scope)
	}
	// An intermediate rotation rolls the intermediate KEY, which would orphan the
	// old-key leaves the dual-root overlap promises keep verifying. Refuse it mid
	// overlap; the operator finalizes the root rotation first.
	if len(p.PreviousRoots) > 0 {
		return fmt.Errorf("PKI %q is in a root overlap — finalize it (`inforge pki rotate %s %s --root --finalize`) before rotating an intermediate", name, env, name)
	}

	mat, err := mintScopeIntermediate(store, p, name, scope)
	if err != nil {
		return err
	}
	p.Intermediates[scope] = mat
	store.Set(name, p)
	return store.Save(path)
}

// mintScopeIntermediate signs a fresh intermediate (new key) for scope from the
// cold root and re-encrypts its key to the CI recipient. It is the single
// custody seam — decrypt the root with the offline INFORGE_PKI_ROOT_KEY, sign,
// re-encrypt to CI — shared by the first-mint path (runPkiIntermediate) and the
// re-mint paths (rotate/recover). Keeping the offline-identity decrypt, the CN
// convention, and the CI recipient in one place means a change to any of them
// cannot drift between minting and rotating. The caller owns the create-vs-
// overwrite guard and the store write.
func mintScopeIntermediate(store *pki.Store, p pki.PKI, name, scope string) (pki.Material, error) {
	rootIdentity, err := pki.RootIdentityFromEnv()
	if err != nil {
		return pki.Material{}, err
	}
	rootKeyPEM, err := secretstore.Decrypt(p.Root.Key, rootIdentity)
	if err != nil {
		return pki.Material{}, fmt.Errorf("decrypt root key for PKI %q (is %s the right offline root identity?): %w", name, pki.RootIdentityEnvVar, err)
	}
	rootCert, err := pki.ParseCertificate(p.Root.Cert)
	if err != nil {
		return pki.Material{}, err
	}
	rootSigner, err := pki.ParsePrivateKey(string(rootKeyPEM))
	if err != nil {
		return pki.Material{}, err
	}
	certPEM, keyPEM, err := pki.GenerateIntermediate(rootCert, rootSigner, fmt.Sprintf("%s %s intermediate", name, scope))
	if err != nil {
		return pki.Material{}, err
	}
	ciphertext, err := secretstore.Encrypt([]byte(keyPEM), store.IntermediateKeyRecipient())
	if err != nil {
		return pki.Material{}, err
	}
	return pki.Material{Cert: certPEM, Key: ciphertext}, nil
}

// requireRootCustody proves the caller holds the offline cold-root identity by
// decrypting the PKI's current root key with it. It gates every root-custody
// decision — both starting a dual-root overlap and finalizing it — so the same
// check can't drift between the two paths: a weaker finalize gate would let CI
// or a stale checkout retire the old root the begin step minted under a stricter
// one.
func requireRootCustody(p pki.PKI, name string) error {
	rootIdentity, err := pki.RootIdentityFromEnv()
	if err != nil {
		return err
	}
	if _, err := secretstore.Decrypt(p.Root.Key, rootIdentity); err != nil {
		return fmt.Errorf("decrypt current root key for PKI %q (is %s the offline root identity?): %w", name, pki.RootIdentityEnvVar, err)
	}
	return nil
}

// runPkiRotateRoot performs a dual-root rotation of a two-tier PKI's cold root.
//
// Without --finalize it BEGINS the overlap: it verifies the caller holds the
// current cold root (decrypts it with INFORGE_PKI_ROOT_KEY — only the root
// custodian may rotate the root), mints a fresh cold root, retains the old root
// in PreviousRoots, and re-signs every intermediate from the new root while
// PRESERVING each intermediate's key. Key preservation is what makes the overlap
// graceful: live leaves keep verifying (the mesh anchors on the intermediate's
// public key), and a root-anchoring consumer that trusts both roots accepts a
// chain to either. The old intermediate certs are retained in
// PreviousIntermediates so a chain to the old root still validates during the
// window.
//
// With --finalize it ENDS the overlap: once every root-anchoring consumer trusts
// the new root, it drops the retained old root and old intermediate certs.
func runPkiRotateRoot(dir, env, name string, finalize bool) error {
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
		return fmt.Errorf("PKI %q is %s; --root rotation applies to two-tier (mesh) PKIs", name, p.Topology)
	}

	if finalize {
		if len(p.PreviousRoots) == 0 {
			return fmt.Errorf("PKI %q is not in a root overlap — nothing to finalize", name)
		}
		// Dropping the old root is a root-custody decision, just like starting
		// the overlap — gate it on the offline identity so CI (or a stale repo
		// checkout) cannot retire the old root before consumers have migrated.
		if err := requireRootCustody(p, name); err != nil {
			return err
		}
		p.PreviousRoots = nil
		p.PreviousIntermediates = nil
		store.Set(name, p)
		if err := store.Save(path); err != nil {
			return err
		}
		fmt.Printf("finalized root rotation for PKI %q — old root dropped; only the new root remains\n", name)
		fmt.Println("\ncommit the store file. Verify no consumer still anchors solely on the old root before deploying.")
		return nil
	}

	if len(p.PreviousRoots) > 0 {
		return fmt.Errorf("PKI %q is already in a root overlap — finalize it (`inforge pki rotate %s %s --root --finalize`) before starting another", name, env, name)
	}

	// Custody gate: only the holder of the current cold root may rotate it.
	if err := requireRootCustody(p, name); err != nil {
		return err
	}

	// Mint the new cold root, encrypted to the same offline recipient.
	newCertPEM, newKeyPEM, err := pki.GenerateRoot(name + " root")
	if err != nil {
		return err
	}
	newRootCiphertext, err := secretstore.Encrypt([]byte(newKeyPEM), store.RootKeyRecipient(pki.TopologyTwoTier))
	if err != nil {
		return err
	}
	newRootCert, err := pki.ParseCertificate(newCertPEM)
	if err != nil {
		return err
	}
	newRootSigner, err := pki.ParsePrivateKey(newKeyPEM)
	if err != nil {
		return err
	}

	// Re-sign every intermediate from the new root, preserving each key. Sorted
	// for a deterministic diff.
	scopes := make([]string, 0, len(p.Intermediates))
	for s := range p.Intermediates {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	if p.PreviousIntermediates == nil && len(scopes) > 0 {
		p.PreviousIntermediates = map[string][]string{}
	}
	for _, scope := range scopes {
		inter := p.Intermediates[scope]
		existing, err := pki.ParseCertificate(inter.Cert)
		if err != nil {
			return fmt.Errorf("scope %q: %w", scope, err)
		}
		reSignedPEM, err := pki.ReSignIntermediate(newRootCert, newRootSigner, existing)
		if err != nil {
			return fmt.Errorf("scope %q: %w", scope, err)
		}
		p.PreviousIntermediates[scope] = append(p.PreviousIntermediates[scope], inter.Cert)
		inter.Cert = reSignedPEM // key (inter.Key) deliberately unchanged
		p.Intermediates[scope] = inter
	}

	// Retain the old root, then swap in the new one.
	p.PreviousRoots = append(p.PreviousRoots, p.Root)
	p.Root = pki.Material{Cert: newCertPEM, Key: newRootCiphertext}
	store.Set(name, p)
	if err := store.Save(path); err != nil {
		return err
	}

	fmt.Printf("rotated the root of PKI %q — dual-root overlap is now active\n", name)
	fmt.Printf("  re-signed %d intermediate(s) from the new root (keys preserved; live leaves still verify)\n", len(scopes))
	fmt.Println("\nduring the overlap, BOTH roots verify. Next steps:")
	fmt.Println("  1. commit the store file")
	fmt.Println("  2. distribute the NEW root cert to every root-anchoring consumer (e.g. the daemon fleet) so")
	fmt.Println("     it trusts {old, new}. Mesh services need no change — they anchor on intermediate keys.")
	fmt.Printf("  3. once every consumer trusts the new root, end the overlap: inforge pki rotate %s %s --root --finalize\n", env, name)
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

func newPkiRenewCmd(dir *string) *cobra.Command {
	return &cobra.Command{
		Use:           "renew <env>",
		Short:         "Mint fresh mesh leaf certificates for every service and write them to the secrets provider",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPkiRenew(cmd.Context(), *dir, args[0])
		},
	}
}

// runPkiRenew mints a fresh short-TTL leaf for every service's mesh `pki:` from
// the committed per-scope intermediate (decrypting the intermediate key with the
// CI identity, INFORGE_SECRETS_KEY) and writes the material to the secrets
// provider: each mesh host's per-host aggregate (every co-located service's
// leaf + the shared trust bundle, under the mesh workspace's /<hostKey> — the
// pull the mesh proxies converge on, ADR-0033), plus the per-service
// /<svc>/mtls copy for mtls_files: opted-in services. It never runs the infra
// program, so it is safe to schedule (cron calling the CLI) without pushing
// un-shipped infra changes; hosts converge on their own (their daily
// wardnet-mesh-renew timer). A global service gets one leaf (scope "global");
// a regional service gets one per region (the regional set deploys to every
// region).
func runPkiRenew(ctx context.Context, dir, env string) error {
	globalRes, err := loader.LoadGlobalResources(env, dir)
	if err != nil {
		return err
	}
	regionalRes, err := loader.LoadResources(env, dir)
	if err != nil {
		return err
	}
	count, err := renewMeshCerts(ctx, dir, env, globalRes, regionalRes)
	if err != nil {
		return err
	}
	fmt.Printf("renewed %d mesh leaf certificate(s) in %s\n", count, env)
	return nil
}

// renewMeshCerts mints + writes fresh mesh material for an env: the per-host
// mesh aggregates plus the mtls_files per-service copies, across each scope.
// Shared by `inforge pki renew` and the deploy baseline (`inforge deploy` /
// `inforge ephemeral up` post-up). Returns the number of leaves minted.
func renewMeshCerts(ctx context.Context, dir, env string, global, regional types.Resources) (int, error) {
	// A static env reads its config and mints its identity under the same name.
	return renewMeshCertsAs(ctx, dir, env, env, global, regional, "")
}

// renewMeshCertsAs is the config/identity-decoupled core of renewMeshCerts
// (ADR-0028). configEnv selects the source of truth — the pki.enc.yaml store,
// variables.yaml, and regions.yaml under resources/<configEnv>/ — while
// identityEnv is the env stamped into each minted leaf's SPIFFE ID and the
// Infisical workspace the cert is written to. A static env passes configEnv ==
// identityEnv. An ephemeral env passes configEnv = the source env (whose PKI it
// clones) and identityEnv = its own slug, so its services get their own trust
// scope (spiffe://…/<slug>/…) signed by the source's intermediate, written to the
// slug-scoped workspace the ephemeral deploy provisioned.
//
// only narrows the run to one service ("" = all): the per-service mode the
// release paths use to refresh an mtls_files service's /<svc>/mtls copy before
// its unit restarts. Per-service mode never touches the per-host mesh
// aggregates — release delivery doesn't change mesh identity; the mesh material
// is the deploy baseline's and the renew cron's business.
func renewMeshCertsAs(ctx context.Context, dir, configEnv, identityEnv string, global, regional types.Resources, only string) (int, error) {
	// Nothing to write anywhere → no-op before touching the store or the CI
	// identity, so e.g. releasing a service with no service-side mtls files
	// needs no INFORGE_SECRETS_KEY.
	if !renewScopeHasWork(global, only) && !renewScopeHasWork(regional, only) {
		return 0, nil
	}
	store, err := pki.Load(pki.Path(dir, configEnv))
	if err != nil {
		if errors.Is(err, pki.ErrNotFound) {
			return 0, fmt.Errorf("%w — run `inforge pki init %s` first", err, configEnv)
		}
		return 0, err
	}
	ciIdentity, err := secretstore.IdentityFromEnv()
	if err != nil {
		return 0, err
	}
	vars, err := loader.LoadVariables(configEnv, dir)
	if err != nil {
		return 0, err
	}
	regionTable, globalBlock, err := loader.LoadRegionTable(configEnv, dir)
	if err != nil {
		return 0, err
	}
	allRegions := make([]string, 0, len(regionTable))
	for r := range regionTable {
		allRegions = append(allRegions, r)
	}
	sort.Strings(allRegions)

	// Decrypt each (pki, scope) intermediate at most once per run — many services
	// in a scope mint from the same one.
	type interKey struct{ pkiName, scope string }
	type interVal struct {
		cert *x509.Certificate
		key  crypto.Signer
	}
	interCache := map[interKey]interVal{}
	intermediate := func(pkiName, scope string) (*x509.Certificate, crypto.Signer, error) {
		k := interKey{pkiName, scope}
		if v, ok := interCache[k]; ok {
			return v.cert, v.key, nil
		}
		cert, key, err := meshcert.IntermediateSigner(store, pkiName, scope, ciIdentity)
		if err != nil {
			return nil, nil, err
		}
		interCache[k] = interVal{cert, key}
		return cert, key, nil
	}

	count := 0
	// mint is the shared leaf mint for one (service, scope).
	mint := func(svc types.ServiceSpec, scope string) (leafPEM, keyPEM string, err error) {
		interCert, interKey, err := intermediate(svc.Pki, scope)
		if err != nil {
			return "", "", fmt.Errorf("service %q scope %q: %w", svc.Name, scope, err)
		}
		leafPEM, keyPEM, err = meshcert.MintLeaf(interCert, interKey, vars.BaseDomain, identityEnv, scope, svc.Name)
		if err != nil {
			return "", "", fmt.Errorf("service %q scope %q: %w", svc.Name, scope, err)
		}
		count++
		return leafPEM, keyPEM, nil
	}

	// Failures accumulate instead of aborting: one host's missing workspace (or
	// one service's bad write) must not kill the renewal of every OTHER
	// already-provisioned host and mtls_files service — a scheduled renew that
	// aborts on the first error silently starves the rest of the env of fresh
	// leaves until the 90-day expiry. Everything that could be written is
	// written; the joined error reports everything that couldn't.
	var errs []error

	// renewSet writes one scope's material, reusing one authenticated writer:
	// (1) the per-host mesh aggregates — every co-located pki: service's leaf,
	// the north-south gateway's client leaf (<scope>/gateway) on its host, and
	// the host's trust bundle under /<hostKey> in the mesh workspace, grouped by
	// the SAME meshplan derivation the deploy program realizes the proxies from
	// (rule mesh-host-grouping-is-single-sourced) — skipped in per-service mode;
	// (2) the /<svc>/mtls copy for each mtls_files: service (its own raw-plane
	// leaf, minted independently of the mesh's copy). A host whose material
	// can't be fully assembled is skipped whole — the projection on the host is
	// an atomic set, so a partial aggregate would stall its pull entirely.
	renewSet := func(writer *infisical.CertWriter, res types.Resources, scope string) {
		if only == "" {
			canonical := naming.CanonicalComputeKeys(res.Compute)
			byHost := meshplan.ServicesByHost(res, canonical)
			gwByHost := meshplan.GatewayMemberByHost(res, canonical)
		hosts:
			for _, hostKey := range meshplan.UnionHostKeys(byHost, gwByHost) {
				members := byHost[hostKey]
				if gw, ok := gwByHost[hostKey]; ok {
					members = append(append([]types.ServiceSpec(nil), members...), gw)
				}
				leaves := map[string]map[string]string{}
				pkiSeen := map[string]bool{}
				var pkiNames []string
				for _, svc := range members {
					leafPEM, keyPEM, err := mint(svc, scope)
					if err != nil {
						errs = append(errs, err)
						continue hosts
					}
					leaves[svc.Name] = map[string]string{
						meshpaths.LeafCertFile: leafPEM,
						meshpaths.LeafKeyFile:  keyPEM,
					}
					if !pkiSeen[svc.Pki] {
						pkiSeen[svc.Pki] = true
						pkiNames = append(pkiNames, svc.Pki)
					}
				}
				// One bundle per host: the concatenated trust sets of every mesh its
				// services belong to (an env may host several meshes; the proxy has a
				// single trust anchor path).
				sort.Strings(pkiNames)
				var bundle strings.Builder
				for _, pkiName := range pkiNames {
					b, err := store.TrustBundle(pkiName, meshcert.TrustSet(scope, allRegions))
					if err != nil {
						errs = append(errs, fmt.Errorf("host %q scope %q: %w", hostKey, scope, err))
						continue hosts
					}
					bundle.WriteString(b)
				}
				if err := writer.WriteMeshHost(ctx, hostKey, leaves, bundle.String()); err != nil {
					errs = append(errs, fmt.Errorf("host %q scope %q: write mesh material: %w", hostKey, scope, err))
				}
			}
		}
		for _, svc := range res.Service {
			if svc.Pki == "" || !svc.MtlsFiles {
				continue
			}
			if only != "" && svc.Name != only {
				continue
			}
			leafPEM, keyPEM, err := mint(svc, scope)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			bundle, err := store.TrustBundle(svc.Pki, meshcert.TrustSet(scope, allRegions))
			if err != nil {
				errs = append(errs, fmt.Errorf("service %q scope %q: %w", svc.Name, scope, err))
				continue
			}
			files := meshcert.CertFiles(leafPEM, keyPEM, bundle)
			if err := writer.Write(ctx, svc.Container, svc.Name, meshcert.MtlsDir, files); err != nil {
				errs = append(errs, fmt.Errorf("service %q scope %q: write certs: %w", svc.Name, scope, err))
			}
		}
	}

	// Global services: scope "global", region-less slug, creds from the global block.
	if renewScopeHasWork(global, only) {
		if globalBlock == nil {
			return 0, fmt.Errorf("a global service declares pki but the env has no global providers block")
		}
		cID, cSecret, site, org, err := requireInfisicalCreds(globalBlock.Providers, "global")
		if err != nil {
			return 0, err
		}
		writer, err := infisical.NewCertWriter(ctx, identityEnv, "", cID, cSecret, site, org)
		if err != nil {
			errs = append(errs, fmt.Errorf("scope global: %w", err))
		} else {
			renewSet(writer, global, pki.ScopeGlobal)
		}
	}
	// Regional services: one leaf per region, scope = region, per-region slug + creds.
	if renewScopeHasWork(regional, only) {
		for _, region := range allRegions {
			ar := regionTable[region]
			cID, cSecret, site, org, err := requireInfisicalCreds(ar.Providers, region)
			if err != nil {
				return 0, err
			}
			writer, err := infisical.NewCertWriter(ctx, identityEnv, ar.Slug, cID, cSecret, site, org)
			if err != nil {
				errs = append(errs, fmt.Errorf("region %q: %w", region, err))
				continue
			}
			renewSet(writer, regional, region)
		}
	}
	return count, errors.Join(errs...)
}

// renewScopeHasWork reports whether a scope has anything to write — gating both
// the whole-run no-op fast path (before the store or CI identity are touched)
// and each scope's provider-credential requirement. In per-service mode (only
// != "") the sole possible write is the named service's /<svc>/mtls copy, so
// only an mtls_files: match counts; in full mode any pki: member does (the
// per-host mesh aggregates cover every mesh member), and so does a north-south
// gateway — its client leaf (<scope>/gateway) is part of its host's aggregate.
func renewScopeHasWork(res types.Resources, only string) bool {
	for _, s := range res.Service {
		if s.Pki == "" {
			continue
		}
		if only != "" {
			if s.Name == only && s.MtlsFiles {
				return true
			}
			continue
		}
		return true
	}
	return only == "" && len(res.Gateway) > 0
}

// requireInfisicalCreds extracts the Infisical admin universal-auth credentials
// from a providers block (regions.yaml global or per-region), erroring when the
// block is missing clientId/clientSecret so the misconfigured scope is named up
// front rather than surfacing as a generic auth failure deep in the write.
func requireInfisicalCreds(providers map[string]map[string]any, scope string) (clientID, clientSecret, siteURL, orgID string, err error) {
	cfg := providers["infisical"]
	get := func(k string) string { s, _ := cfg[k].(string); return s }
	clientID, clientSecret, siteURL, orgID = get("clientId"), get("clientSecret"), get("siteUrl"), get("organizationId")
	if clientID == "" || clientSecret == "" {
		return "", "", "", "", fmt.Errorf("scope %q has no infisical clientId/clientSecret in its regions.yaml providers block", scope)
	}
	return clientID, clientSecret, siteURL, orgID, nil
}
