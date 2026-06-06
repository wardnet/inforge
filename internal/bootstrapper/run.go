package bootstrapper

import (
	"context"
	"fmt"
	"path/filepath"
)

// Descriptor and credential filenames within a service's on-host directory.
const (
	descriptorFile = "descriptor.yaml"
	credentialFile = "credential.age"
)

// Run is the inforge-bootstrap entry point. It is invoked by systemd as
// ExecStart=/usr/local/bin/inforge-bootstrap <service-dir>: load the descriptor,
// resolve the run-as user, decrypt the provider credential with the host key,
// fetch the service's secrets (with bounded backoff), build the env, then drop
// privilege and exec the real service binary. Any error returns to main, which
// exits non-zero so systemd restarts the unit.
func Run(args []string, version string) error {
	if len(args) == 2 && (args[1] == "--version" || args[1] == "version") {
		fmt.Println(version)
		return nil
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: inforge-bootstrap <service-descriptor-dir>")
	}
	dir := args[1]
	ctx := context.Background()

	desc, err := LoadDescriptor(filepath.Join(dir, descriptorFile))
	if err != nil {
		return err
	}

	user, err := lookupUser(desc.User)
	if err != nil {
		return err
	}

	credential, err := DecryptCredential(filepath.Join(dir, credentialFile), defaultHostKeyPath)
	if err != nil {
		return err
	}

	fetcher, err := newFetcher(desc.Provider, credential)
	if err != nil {
		return err
	}

	secrets, err := FetchWithBackoff(ctx, fetcher, realClock{}, baseDelay, maxDelay, budget)
	if err != nil {
		return err
	}

	envv, err := buildEnv(desc, secrets, user.home)
	if err != nil {
		return err
	}

	return dropAndExec(desc.Exec, user.uid, user.gid, envv)
}

// newFetcher selects the SecretsFetcher implementation for the provider kind.
// New providers are added here as new implementations.
func newFetcher(p Provider, credential []byte) (SecretsFetcher, error) {
	switch p.Kind {
	case "infisical":
		return newInfisicalFetcher(p, credential)
	default:
		return nil, fmt.Errorf("unsupported provider kind %q", p.Kind)
	}
}
