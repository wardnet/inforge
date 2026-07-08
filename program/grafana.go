package program

import (
	"fmt"

	grafanasdk "github.com/pulumiverse/pulumi-grafana/sdk/go/grafana"
	"github.com/pulumiverse/pulumi-grafana/sdk/go/grafana/oss"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/wardnet/inforge/internal/grafana"
	"github.com/wardnet/inforge/internal/grafanadash"
	"github.com/wardnet/inforge/internal/loader"
	"github.com/wardnet/inforge/internal/types"
)

// realizeGrafana pushes this env's built-in dashboards to Grafana via the
// pulumiverse/grafana provider (ADR-0038). It is org-global — created ONCE per run,
// outside the per-scope loop — and gated on observability.grafana_url: absent, it is a
// no-op. The service-account token is the reserved secret observability/grafana_token
// (grafana.TokenKey), decrypted like the OTLP credential; a URL set with no token is a
// hard misconfiguration (fail at up, skipped in preview). Everything is env-prefixed
// (folder + dashboard UIDs) so multiple inforge envs share one Grafana org cleanly.
func realizeGrafana(ctx *pulumi.Context, dir, srcEnv, env string, obs types.ObservabilityConfig, dryRun bool) error {
	if obs.GrafanaURL == "" {
		return nil
	}

	tokenRaw, err := decryptReservedSecret(dir, srcEnv, grafana.ReservedNamespace, grafana.TokenKey, dryRun)
	if err != nil {
		return err
	}
	if tokenRaw == "" && !dryRun {
		return fmt.Errorf("observability: grafana_url is set but secrets.enc.yaml is missing or has no %s/%s token — run `inforge secret set %s %s %s --reserved` and commit the store", grafana.ReservedNamespace, grafana.TokenKey, srcEnv, grafana.ReservedNamespace, grafana.TokenKey)
	}

	prov, err := grafanasdk.NewProvider(ctx, "grafana", &grafanasdk.ProviderArgs{
		Url:  pulumi.String(obs.GrafanaURL),
		Auth: pulumi.ToSecret(pulumi.String(tokenRaw)).(pulumi.StringOutput),
	})
	if err != nil {
		return fmt.Errorf("observability: grafana provider: %w", err)
	}

	folder, err := oss.NewFolder(ctx, "grafana-folder", &oss.FolderArgs{
		Uid:   pulumi.String(grafana.FolderUID(env)),
		Title: pulumi.String(grafana.FolderTitle(env)),
	}, pulumi.Provider(prov))
	if err != nil {
		return fmt.Errorf("observability: grafana folder: %w", err)
	}

	newDashboard := func(name, body string) error {
		if _, err := oss.NewDashboard(ctx, "grafana-dash-"+name, &oss.DashboardArgs{
			ConfigJson: pulumi.String(body),
			Folder:     folder.Uid,
			Overwrite:  pulumi.Bool(true),
		}, pulumi.Provider(prov)); err != nil {
			return fmt.Errorf("observability: grafana dashboard %s: %w", name, err)
		}
		return nil
	}

	// Built-in dashboards, generated from the metrics inforge owns.
	builtins := []struct {
		kind   string
		render func(env, uid string) (string, error)
	}{
		{"infrastructure", grafanadash.Infrastructure},
		{"database", grafanadash.Database},
		{"service", grafanadash.Service},
	}
	for _, b := range builtins {
		body, err := b.render(env, grafana.DashboardUID(env, b.kind))
		if err != nil {
			return err
		}
		if err := newDashboard(b.kind, body); err != nil {
			return err
		}
	}

	// Custom dashboards: Grafana-exported files committed under this env's
	// observability/dashboards/ directory (read from the config-source env, ADR-0028).
	// Each is normalized (parsed + env-prefixed uid) and pushed into the same folder.
	custom, err := loader.LoadCustomDashboards(srcEnv, dir)
	if err != nil {
		return err
	}
	for _, c := range custom {
		body, err := grafanadash.Custom(c.Name, grafana.DashboardUID(env, "custom-"+c.Name), c.Data, c.IsYAML)
		if err != nil {
			return err
		}
		if err := newDashboard("custom-"+c.Name, body); err != nil {
			return err
		}
	}
	return nil
}
