---
sidebar_position: 4
---

# deploy.yml

Deploys infrastructure changes for one environment using `inforge deploy`.

## Usage

```yaml
uses: wardnet/inforge/.github/workflows/deploy.yml@v1
with:
  environment: prd
  stack_config: inforge.prd.yaml
secrets: inherit
```

## What it does

1. Checks out the repo and installs `inforge`
2. Runs `inforge deploy --yes --stack <environment> --output json`
3. Renders an HTML summary report (same format as preview)
4. Posts the report to `$GITHUB_STEP_SUMMARY` and as a commit comment

## Inputs

| Input | Type | Required | Description |
|-------|------|----------|-------------|
| `environment` | string | Yes | Stack name / environment (e.g. `prd`) |
| `stack_config` | string | Yes | Path to the stack config file |

## Secrets (pass via `secrets: inherit`)

Same as [preview.yml](./preview).

## Bootstrap

If the stack has secret values, the deploy workflow fetches a GitHub Actions OIDC token
and passes it as `inforge:oidc_token` to authenticate the key broker call. This is handled
automatically — the `id-token: write` permission is set in the workflow.

## State management

After a successful deploy:

- The deploy descriptor is written to `deploy/<env>.yaml`
- If the backend is `git-branch`: updated state is pushed to the state branch automatically
