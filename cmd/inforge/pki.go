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
		return fmt.Errorf("PKI %q already has an intermediate for scope %q (rotation is a separate command)", name, scope)
	}

	rootIdentity, err := pki.RootIdentityFromEnv()
	if err != nil {
		return err
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
// CI identity, INFORGE_SECRETS_KEY) and writes the leaf + key + per-scope trust
// bundle to the secrets provider. It never runs the infra program, so it is safe
// to schedule (cron calling the CLI) without pushing un-shipped infra changes.
// A global service gets one leaf (scope "global"); a regional service gets one
// per region (the regional set deploys to every region).
func runPkiRenew(ctx context.Context, dir, env string) error {
	globalRes, err := loader.LoadGlobalResources(env, dir)
	if err != nil {
		return err
	}
	regionalRes, err := loader.LoadResources(env, dir)
	if err != nil {
		return err
	}
	count, err := renewMeshCerts(ctx, dir, env, globalRes.Service, regionalRes.Service)
	if err != nil {
		return err
	}
	fmt.Printf("renewed %d mesh leaf certificate(s) in %s\n", count, env)
	return nil
}

// renewMeshCerts mints + writes a fresh leaf (and per-scope trust bundle) for
// every pki-bearing service in globalSvcs/regionalSvcs, across each service's
// scopes. Shared by `inforge pki renew` (all services) and `inforge releases
// deploy` (just the released service, so the first release — and every update —
// restarts into a provider that already holds the service's leaf). Returns the
// number of leaves written.
func renewMeshCerts(ctx context.Context, dir, env string, globalSvcs, regionalSvcs []types.ServiceSpec) (int, error) {
	store, err := pki.Load(pki.Path(dir, env))
	if err != nil {
		if errors.Is(err, pki.ErrNotFound) {
			return 0, fmt.Errorf("%w — run `inforge pki init %s` first", err, env)
		}
		return 0, err
	}
	ciIdentity, err := secretstore.IdentityFromEnv()
	if err != nil {
		return 0, err
	}
	vars, err := loader.LoadVariables(env, dir)
	if err != nil {
		return 0, err
	}
	regionTable, global, err := loader.LoadRegionTable(env, dir)
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
	// renewSet mints + writes a leaf for each pki-bearing service in one scope,
	// reusing one authenticated provider writer.
	renewSet := func(writer *infisical.CertWriter, services []types.ServiceSpec, scope string) error {
		for _, svc := range services {
			if svc.Pki == "" {
				continue
			}
			interCert, interKey, err := intermediate(svc.Pki, scope)
			if err != nil {
				return fmt.Errorf("service %q scope %q: %w", svc.Name, scope, err)
			}
			leafPEM, keyPEM, err := meshcert.MintLeaf(interCert, interKey, vars.BaseDomain, env, scope, svc.Name)
			if err != nil {
				return fmt.Errorf("service %q scope %q: %w", svc.Name, scope, err)
			}
			bundle, err := store.TrustBundle(svc.Pki, meshcert.TrustSet(scope, allRegions))
			if err != nil {
				return fmt.Errorf("service %q scope %q: %w", svc.Name, scope, err)
			}
			files := meshcert.CertFiles(leafPEM, keyPEM, bundle)
			if err := writer.Write(ctx, svc.Container, svc.Name, meshcert.MtlsDir, files); err != nil {
				return fmt.Errorf("service %q scope %q: write certs: %w", svc.Name, scope, err)
			}
			count++
		}
		return nil
	}

	// Global services: scope "global", region-less slug, creds from the global block.
	if anyServiceHasPki(globalSvcs) {
		if global == nil {
			return 0, fmt.Errorf("a global service declares pki but the env has no global providers block")
		}
		cID, cSecret, site, org, err := requireInfisicalCreds(global.Providers, "global")
		if err != nil {
			return 0, err
		}
		writer, err := infisical.NewCertWriter(ctx, env, "", cID, cSecret, site, org)
		if err != nil {
			return 0, err
		}
		if err := renewSet(writer, globalSvcs, pki.ScopeGlobal); err != nil {
			return 0, err
		}
	}
	// Regional services: one leaf per region, scope = region, per-region slug + creds.
	if anyServiceHasPki(regionalSvcs) {
		for _, region := range allRegions {
			ar := regionTable[region]
			cID, cSecret, site, org, err := requireInfisicalCreds(ar.Providers, region)
			if err != nil {
				return 0, err
			}
			writer, err := infisical.NewCertWriter(ctx, env, ar.Slug, cID, cSecret, site, org)
			if err != nil {
				return 0, err
			}
			if err := renewSet(writer, regionalSvcs, region); err != nil {
				return 0, err
			}
		}
	}
	return count, nil
}

func anyServiceHasPki(services []types.ServiceSpec) bool {
	for _, s := range services {
		if s.Pki != "" {
			return true
		}
	}
	return false
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
