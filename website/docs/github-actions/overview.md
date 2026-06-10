---
sidebar_position: 1
---

# GitHub Actions Overview

inforge is a toolkit, not a set of opinionated pipelines. It ships **one** GitHub Action — a
composite action that installs the `inforge` CLI — and you own your workflows. Your workflow injects
your secrets as environment variables and runs `inforge <command>`; inforge never enumerates a fixed
list of provider secrets, so it stays decoupled from which clouds you use.

## The install action

```yaml
- uses: wardnet/inforge@v1
```

Downloads the `inforge` binary and runs `inforge plugins install`. Pin the CLI version with the
`version` input:

```yaml
- uses: wardnet/inforge@v1
  with:
    version: v1.6.0   # default: latest release
```

That is the whole toolkit-provided surface. Everything else is a normal `run:` step calling the CLI.

## How secrets reach inforge

inforge resolves `${ENV_VAR}` references in your `regions.yaml`, `variables.yaml`, and secret
`source:` fields from the **process environment**. So you decide the vocabulary: set whatever
environment variables your config references, from whatever secrets you keep next to your infra
definition. inforge does not know — and does not need to know — that a secret is named
`CLOUDFLARE_API_TOKEN`.

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    env:                                       # your names, your secrets
      HCLOUD_TOKEN:         ${{ secrets.HCLOUD_TOKEN }}
      CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }}
      NEON_API_KEY:         ${{ secrets.NEON_API_KEY }}
      # ...only what your regions.yaml / secrets reference
```

Add a provider → add one `env:` line. Drop one → delete a line. The toolkit never changes.

## Starter workflows

Copy these into your infrastructure repo and adjust the `env:` block to your providers.

```yaml title=".github/workflows/pr.yml"
name: PR Checks
on:
  pull_request:
    paths: ["resources/**", "inforge*.yaml"]

permissions:
  contents: read
  pull-requests: write

jobs:
  preview:
    runs-on: ubuntu-latest
    env:
      PULUMI_CONFIG_PASSPHRASE: ${{ secrets.PULUMI_CONFIG_PASSPHRASE }}
      HCLOUD_TOKEN:             ${{ secrets.HCLOUD_TOKEN }}
      CLOUDFLARE_API_TOKEN:     ${{ secrets.CLOUDFLARE_API_TOKEN }}
      CLOUDFLARE_ZONE_ID:       ${{ secrets.CLOUDFLARE_ZONE_ID }}
      NEON_API_KEY:             ${{ secrets.NEON_API_KEY }}
      INFISICAL_CLIENT_ID:      ${{ secrets.INFISICAL_CLIENT_ID }}
      INFISICAL_CLIENT_SECRET:  ${{ secrets.INFISICAL_CLIENT_SECRET }}
    steps:
      - uses: actions/checkout@v4
      - uses: wardnet/inforge@v1
      - run: inforge validate --stack prd
      - run: inforge preview --stack prd --report report.md
      - if: github.event_name == 'pull_request'
        run: gh pr comment "${{ github.event.pull_request.number }}" --body-file report.md
        env:
          GH_TOKEN: ${{ github.token }}
```

```yaml title=".github/workflows/deploy.yml"
name: Deploy
on:
  push:
    branches: [main]
    paths: ["resources/**", "inforge*.yaml"]

permissions:
  contents: read

jobs:
  deploy:
    runs-on: ubuntu-latest
    env:
      PULUMI_CONFIG_PASSPHRASE:   ${{ secrets.PULUMI_CONFIG_PASSPHRASE }}
      HCLOUD_TOKEN:               ${{ secrets.HCLOUD_TOKEN }}
      CLOUDFLARE_API_TOKEN:       ${{ secrets.CLOUDFLARE_API_TOKEN }}
      CLOUDFLARE_ZONE_ID:         ${{ secrets.CLOUDFLARE_ZONE_ID }}
      NEON_API_KEY:               ${{ secrets.NEON_API_KEY }}
      INFISICAL_CLIENT_ID:        ${{ secrets.INFISICAL_CLIENT_ID }}
      INFISICAL_CLIENT_SECRET:    ${{ secrets.INFISICAL_CLIENT_SECRET }}
      INFORGE_DEPLOY_PRIVATE_KEY: ${{ secrets.DEPLOY_PRIVATE_KEY }}
    steps:
      - uses: actions/checkout@v4
      - uses: wardnet/inforge@v1
      - run: inforge deploy --yes --stack prd
```

`inforge deploy`/`preview` always write a markdown run report to `--report <path>` (or a temp file,
whose path they print) and, when `$GITHUB_STEP_SUMMARY` is set, append it to the job summary
automatically. Posting it as a PR comment is a one-liner (`gh pr comment --body-file`) you own — the
CLI never calls the GitHub API itself.

## Notes

- The environment is the Pulumi **stack name** (`--stack prd`); you do not need an `inforge.<env>.yaml`
  just to name it.
- `${ENV_VAR}` references that are unset fail the run loudly — only set the ones your config uses.
- Backend credentials (`AWS_*`, `CLOUDFLARE_ACCOUNT_ID`) are needed only for an `r2`/`s3` state
  backend; a `file`/`git-branch` backend needs none.
