package infisical

import (
	"context"

	"github.com/wardnet/inforge/providers/infisical/internal/client"
)

// CertWriter writes mesh leaf material to Infisical imperatively (the
// `inforge pki renew` path, outside Pulumi). It authenticates once and caches
// resolved workspace IDs, so renewing many services that share a container /
// region costs one universal-auth login and one workspace lookup, not one per
// service. It addresses the SAME workspace name, env slug, and service path the
// Pulumi deploy path uses (workspaceName / envToSlug / servicePath), so the host
// reads renewed certs from where deploy provisioned them.
type CertWriter struct {
	adapter *InfisicalSecretsAdapter
	env     string
	client  *client.Client
	wsIDs   map[string]string // container -> workspace ID (per-run cache)
}

// NewCertWriter authenticates a writer for one credentials set (the env's global
// providers block, or a region's). slug is the region slug ("" for the
// region-less global scope), matching deploy's naming.
func NewCertWriter(ctx context.Context, env, slug, clientID, clientSecret, siteURL, orgID string) (*CertWriter, error) {
	a := New(clientID, clientSecret, siteURL, orgID, slug)
	c := client.New(a.siteUrl)
	if err := c.Authenticate(ctx, a.clientId, a.clientSecret); err != nil {
		return nil, err
	}
	return &CertWriter{adapter: a, env: env, client: c, wsIDs: map[string]string{}}, nil
}

// Write upserts files (secret name -> PEM) under "/<service>/<dir>" in the
// container's workspace. The workspace must already exist (deploy owns its
// lifecycle and the per-service identity that reads it) — a missing workspace is
// an error, not a silent create, so renewing a never-deployed service fails loudly.
func (w *CertWriter) Write(ctx context.Context, container, service, dir string, files map[string]string) error {
	wsID, ok := w.wsIDs[container]
	if !ok {
		id, err := w.client.WorkspaceID(ctx, w.adapter.workspaceName(container, w.env))
		if err != nil {
			return err
		}
		w.wsIDs[container] = id
		wsID = id
	}
	return w.client.WriteSecrets(ctx, wsID, envToSlug(w.env), servicePath(service)+"/"+dir, files)
}
