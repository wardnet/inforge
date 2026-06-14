package infisical

import (
	"context"

	"github.com/wardnet/inforge/internal/naming"
	"github.com/wardnet/inforge/providers/infisical/internal/client"
)

// WriteServiceCerts writes a mesh service's PEM material (files keyed by secret
// name, e.g. leaf.crt/leaf.key/bundle.crt) to "/<service>/<dir>" in the
// per-(container, region) Infisical workspace, authenticating with the adapter's
// admin universal-auth credentials. It is the imperative, non-Pulumi write path
// used by `inforge pki renew`: it adopts the workspace by its deterministic
// deploy name (creating it if absent), then upserts the files. The workspace
// name and env slug match exactly what deploy uses, so the bootstrapper reads
// renewed certs from the same location it reads infra secrets.
func (a *InfisicalSecretsAdapter) WriteServiceCerts(ctx context.Context, container, service, env, dir string, files map[string]string) error {
	c := client.New(a.siteUrl)
	if err := c.Authenticate(ctx, a.clientId, a.clientSecret); err != nil {
		return err
	}
	wsName := naming.Resource(env, a.slug, "container", container)
	wsID, err := c.AdoptOrCreateWorkspace(ctx, wsName)
	if err != nil {
		return err
	}
	return c.WriteSecrets(ctx, wsID, envToSlug(env), "/"+service+"/"+dir, files)
}
