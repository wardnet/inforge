---
sidebar_position: 2
---

# inforge preview

Preview infrastructure changes for a stack using the Pulumi Automation API.

## Usage

```bash
inforge preview --stack <env> [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--stack` / `-s` | (required) | Stack name / environment (e.g. `prd`) |
| `--stack-config` | `inforge.<stack>.yaml` | Path to the stack config file |
| `--output` / `-o` | `""` (human) | Output format: `""` for human-readable, `json` for structured JSON |
| `--allow-multiple` | `false` | Allow running when multiple environments have changes detected |
| `--config` / `-c` | `./inforge.yaml` | Path to the project config file |
| `--dir` / `-d` | `./resources` | Path to the resources directory |

## Output

### Human-readable (default)

Streams Pulumi's progress output to stdout — shows each resource being evaluated.

### JSON (`--output json`)

Emits a structured summary to stdout:

```json
{
  "environment": "prd",
  "summary": {
    "create": 2,
    "update": 0,
    "delete": 0,
    "same": 5
  }
}
```

The GitHub Actions preview workflow uses this to render the HTML report in
`$GITHUB_STEP_SUMMARY` and as a PR comment.

## Examples

```bash
# Human-readable preview
inforge preview --stack prd --stack-config inforge.prd.yaml

# JSON output for CI
inforge preview --stack prd --stack-config inforge.prd.yaml --output json
```

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Preview succeeded |
| `1` | Preview failed or error |
