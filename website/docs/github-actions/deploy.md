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

## Secret delivery

For each service whose container declares secrets, the deploy writes the secrets to the provider under
the service's scoped path, mints a per-service machine identity, and writes a secret-free descriptor
plus a host-key-encrypted credential onto the host over SSH. The service fetches its own secrets at
runtime via `inforge-bootstrap`; no OIDC token or key broker is involved. See
[Secrets → How secrets reach a service](../resources/secrets#how-secrets-reach-a-service).

## State management

After a successful deploy:

- The deploy descriptor is written to `deploy/<env>.yaml`
- If the backend is `git-branch`: updated state is pushed to the state branch automatically
