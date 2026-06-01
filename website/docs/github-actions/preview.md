---
sidebar_position: 3
---

# preview.yml

Previews infrastructure changes for one environment using `inforge preview`.

## Usage

```yaml
uses: wardnet/inforge/.github/workflows/preview.yml@v1
with:
  environment: prd
  stack_config: inforge.prd.yaml
secrets: inherit
```

## What it does

1. Checks out the repo and installs `inforge`
2. Runs `inforge preview --stack <environment> --stack-config <stack_config> --output json`
3. Renders an HTML report with change counts (create/update/delete/unchanged)
4. Posts the report to `$GITHUB_STEP_SUMMARY` and as a PR comment

## HTML report

The report is displayed both in the workflow summary and as a PR comment:

```
✅ inforge preview — prd

| ➕ Create | ✏️ Update | 🗑️ Delete | = Same |
|-----------|-----------|-----------|--------|
|     2     |     0     |     0     |   5    |

▶ Full output (collapsible)
```

## Inputs

| Input | Type | Required | Description |
|-------|------|----------|-------------|
| `environment` | string | Yes | Stack name / environment (e.g. `prd`) |
| `stack_config` | string | Yes | Path to the stack config file |

## Secrets (pass via `secrets: inherit`)

| Secret | Description |
|--------|-------------|
| `HCLOUD_TOKEN` | Hetzner Cloud API token |
| `CLOUDFLARE_API_TOKEN` | Cloudflare API token |
| `NEON_API_KEY` | Neon API key |
| `INFISICAL_CLIENT_ID` | Infisical client ID |
| `INFISICAL_CLIENT_SECRET` | Infisical client secret |
