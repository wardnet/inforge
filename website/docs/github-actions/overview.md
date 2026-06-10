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

:::warning `@v1` pins the action, not the CLI
`wardnet/inforge@v1` pins the *action* to the rolling `v1` tag; on its own it installs the **latest**
`inforge` release, which can move under you. For reproducible runs, also pin the CLI with the
`version:` input (e.g. `version: v1.6.0`). The two are independent: the action ref controls the
install glue, `version:` controls the binary it downloads.
:::

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
        with:
          version: v1.6.0          # pin the CLI for reproducible runs
      - run: inforge validate prd
      - run: inforge preview prd --report report.md
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
        with:
          version: v1.6.0
      - run: inforge deploy prd --yes
```

### Scheduled drift reconcile (optional)

`inforge matrix` prints the environments whose resources changed between two git refs, so a scheduled
job can re-deploy only what drifted. Run it on a cron and feed the result into a matrix:

```yaml title=".github/workflows/reconcile.yml"
name: Reconcile
on:
  schedule:
    - cron: "0 4 * * *"
  workflow_dispatch:

permissions:
  contents: read

jobs:
  matrix:
    runs-on: ubuntu-latest
    outputs:
      environments: ${{ steps.m.outputs.environments }}
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: wardnet/inforge@v1
        with:
          version: v1.6.0
      - id: m
        run: echo "environments=$(inforge matrix --base main --head HEAD)" >> "$GITHUB_OUTPUT"

  deploy:
    needs: matrix
    if: needs.matrix.outputs.environments != '[]'
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include: ${{ fromJson(needs.matrix.outputs.environments) }}
    env:                                       # your providers' secrets
      PULUMI_CONFIG_PASSPHRASE:   ${{ secrets.PULUMI_CONFIG_PASSPHRASE }}
      HCLOUD_TOKEN:               ${{ secrets.HCLOUD_TOKEN }}
      CLOUDFLARE_API_TOKEN:       ${{ secrets.CLOUDFLARE_API_TOKEN }}
      NEON_API_KEY:               ${{ secrets.NEON_API_KEY }}
      INFISICAL_CLIENT_ID:        ${{ secrets.INFISICAL_CLIENT_ID }}
      INFISICAL_CLIENT_SECRET:    ${{ secrets.INFISICAL_CLIENT_SECRET }}
      INFORGE_DEPLOY_PRIVATE_KEY: ${{ secrets.DEPLOY_PRIVATE_KEY }}
    steps:
      - uses: actions/checkout@v4
      - uses: wardnet/inforge@v1
        with:
          version: v1.6.0
      - run: inforge deploy "${{ matrix.environment }}" --yes
```

### Service release (optional)

A **service** repo builds its own artifact, then uses inforge to push it to the release store and roll
it out. This lives in the service repo, not the infra repo:

```yaml title=".github/workflows/release.yml"
name: Release
on:
  push:
    branches: [main]

permissions:
  contents: read

concurrency:                                   # one release per service+env at a time
  group: inforge-release-${{ github.ref_name }}
  cancel-in-progress: false

jobs:
  release:
    runs-on: ubuntu-latest
    env:
      PULUMI_CONFIG_PASSPHRASE: ${{ secrets.PULUMI_CONFIG_PASSPHRASE }}
      INFORGE_DEPLOY_PRIVATE_KEY: ${{ secrets.DEPLOY_PRIVATE_KEY }}
    steps:
      - uses: actions/checkout@v4
      # ...your build steps produce the artifact under ./deployments...
      - uses: wardnet/inforge@v1
        with:
          version: v1.6.0
      - run: inforge releases push   prd --service api-server --sha "$GITHUB_SHA"
      - run: inforge releases deploy prd --service api-server --sha "$GITHUB_SHA"
```

`inforge releases deploy` resolves the host, folder, and systemd unit from the infra Pulumi stack at
runtime — nothing about the deploy target needs to be committed to the service repo.

`inforge deploy`/`preview` always write a markdown run report to `--report <path>` (or a temp file,
whose path they print) and, when `$GITHUB_STEP_SUMMARY` is set, append it to the job summary
automatically. Posting it as a PR comment is a one-liner (`gh pr comment --body-file`) you own — the
CLI never calls the GitHub API itself.

## Notes

- The environment is the first positional argument (`inforge deploy prd`) — it is the Pulumi stack
  name. You do not need an `inforge.<env>.yaml` just to name it; if the file is absent, the run uses no
  extra stack config.
- `${ENV_VAR}` references that are unset fail the run loudly — only set the ones your config uses.
- Backend credentials (`AWS_*`, `CLOUDFLARE_ACCOUNT_ID`) are needed only for an `r2`/`s3` state
  backend; a `file`/`git-branch` backend needs none.

## Migrating to 1.6

1.6 makes inforge a provider-agnostic toolkit: it stops shipping reusable workflows that enumerated a
fixed set of provider secrets. Three things change for consumers.

**Reusable workflows → your own workflow + the action.** Replace every
`uses: wardnet/inforge/.github/workflows/<name>.yml@v1` caller with a normal job that installs the CLI
via `wardnet/inforge@v1` and runs `inforge <command>` directly. Start from the [starter
workflows](#starter-workflows) above — the `deploy`, `preview`, `reconcile`, and `service-release`
reusable workflows all map onto one of them.

**`secrets:` → job `env:`.** The reusable workflows took a `secrets:` block and mapped it to env vars
for you. Now you set the `env:` block yourself, naming only the variables your config references. A
provider you don't use is simply a line you don't add.

**`gha:NAME` → `${NAME}`.** The `gha:` secrets-DSL source is gone. In your `secrets/*.yaml` resource
files, rewrite each `source: gha:CLOUDFLARE_API_TOKEN` as `source: ${CLOUDFLARE_API_TOKEN}` — the same
`${ENV_VAR}` form already used in `variables.yaml`/`regions.yaml`. The value still comes from the
process environment, so the matching `env:` line in your workflow is what supplies it. The name must be
upper-snake-case (`[A-Z_][A-Z0-9_]*`); an unset or empty value fails the run rather than writing an
empty secret.
