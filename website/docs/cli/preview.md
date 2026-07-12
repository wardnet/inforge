---
sidebar_position: 2
---

# inforge preview

Preview infrastructure changes for a stack using the Pulumi Automation API.

## Usage

```bash
inforge preview <env> [flags]
```

The environment is a required positional argument (e.g. `prd`) — it is the Pulumi stack name.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--stack-config` | `inforge.<env>.yaml` | Path to the stack config file (optional; a missing default file means no extra config) |
| `--output` / `-o` | `""` (human) | Output format: `""` for human-readable, `json` for structured JSON |
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
