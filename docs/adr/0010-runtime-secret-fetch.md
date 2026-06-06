# Services fetch their own secrets at runtime; inforge bakes nothing

A service's secrets are no longer encrypted into its manifest, escrowed through a key broker, or
re-keyed at first boot. Instead each service fetches its own secrets at process start, directly from
the secrets provider, using a per-service machine identity. inforge's job at deploy time is to write
the provider *coordinates* (never the secret values) onto the host and to mint that identity; nothing
secret is ever baked into an artifact or held by a third party.

This supersedes the SOPS/age manifest-baking model (ADR-0005) and the key-broker + escrow bootstrap
(ADR-0006). The `key-broker/` Cloudflare Worker, the GitHub OIDC-to-broker exchange, and the host
`sops`/`age`/`yq`/`jq` tooling are all removed.

## The model

- **`inforge-bootstrap`** is a small statically-linked Go binary, installed per host and pinned to the
  deploying inforge version. It is the `ExecStart` of every inforge-managed service's systemd unit. At
  start it reads the service's on-host descriptor, fetches the secrets, injects them as environment
  variables, drops privilege to the service's declared user, and `exec`s the real service binary — so
  secret *values* live only in the child process's environment, never on disk, in the journal, or in
  argv.
- **On-host contract** under `/etc/wardnet/services/<svc>/`:
  - `descriptor.yaml` (0644, root) — a versioned, **secret-free** document: the service name, the exec
    path, the run-as user, the provider coordinates (kind, URL, workspace/project, environment, secret
    path), and an env-var → vault-key mapping. It carries no secret values.
  - `credential.age` (0600, root) — the service's machine-identity credential, age-encrypted to the
    host's SSH host key. inforge encrypts it program-side to the host public key it reads over SSH, so
    the plaintext credential never lands on disk and the host needs no `age` binary (the bootstrapper
    decrypts in-process). A **secret-less** service has no `credential.age` at all.
- **Per-service identity.** For each secret-bearing service, inforge mints a machine identity scoped
  read-only to that service's path and writes the service's infra secrets under it. A leaked
  credential exposes only that one service's secrets, and the standing on-host secret is a rotatable
  identity credential, never a secret value.
- **Secret-optional.** A service that declares no secrets gets a descriptor with no provider and no
  env; the bootstrapper skips the fetch entirely and execs with a minimal base environment. Every
  service — with or without secrets — must declare its run-as `user` (the account the bootstrapper
  drops to).

## Considered options & boundaries

- **Rejected baking secret values into the manifest (ADR-0005/0006).** Baking forces a key to travel
  with the artifact and a third party (the broker) to escrow it; it also couples every deploy to an
  OIDC exchange and host re-keying. Runtime fetch keeps secret values out of every inforge artifact
  and removes the broker as a moving part and a trust dependency.
- **Why a host-key-encrypted credential, not the secret values.** The only standing secret on the host
  is the machine identity, which is least-privilege (one service's path) and rotatable. Encrypting it
  to the host's own SSH key means the host can decrypt it with a key it already holds, with no escrow.
- **Why a separate binary as `ExecStart`.** Doing the fetch + privilege-drop + exec in one trusted,
  statically-linked process keeps the security-sensitive path uniform across services and ensures the
  secret values exist only in the final child's environment.

## Consequences

- The producer (`inforge`) and consumer (`inforge-bootstrap`) share one `Descriptor` schema (imported,
  not duplicated), versioned so a fleet on mixed builds never silently misreads a descriptor.
- Hosts install Caddy (for tls-termination) but no secret tooling. The first-boot cloud-init step only
  provisions the deploy user; secret delivery is no longer a first-boot concern.
- CI no longer performs an OIDC-to-broker token exchange. Deploy still SSHes hosts with the deploy
  private key to realize host-level resources and write the descriptor/credential.
