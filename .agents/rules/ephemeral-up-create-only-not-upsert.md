# ephemeral up must create a new stack — never upsert an existing one

`runEphemeralUp` calls `createStack` (which uses `auto.NewStackInlineSource` — fails if the stack
already exists) rather than `upsertStack`. This is an intentional, load-bearing distinction:

- `upsertStack` uses `UpsertStackInlineSource`, which silently adopts a pre-existing stack.
- An existing stack might belong to a **permanent env** whose name happens to match a generated slug,
  or to a previous `up` that crashed mid-provision. Adopting it would stamp `ephemeral=true` +
  `expires_at` onto that stack, making a real environment reaper-eligible.
- Create-only also serves as the concurrency guard: two concurrent `up` calls for the same slug
  can't both win, so the three-signal reaper invariant (ephemeral config is written only to fresh
  ephemeral stacks) is upheld.

## Applies to

`cmd/inforge/ephemeral_up.go` (`runEphemeralUp`) and `cmd/inforge/stack.go` (`createStack`).

## Why

If `up` ever falls back to `upsert`, a slug collision with an existing permanent env silently
marks it as ephemeral and the next `reap` destroys it without confirmation. The only safe sentinel
is creation failure — the operator must choose a different slug or tear down the colliding stack
explicitly.
