# Every Hetzner server joins a spread placement group, bin-packed under the 10-server cap

`HetznerCompute.Create` (`providers/hetzner/compute.go`) sets `PlacementGroupId` on **every**
server it creates, pointing at a Hetzner **spread** placement group (`hcloud.PlacementGroup`,
`Type: "spread"`). A spread group keeps its members on **distinct physical hosts**, reducing
correlated failure. It is always-on — there is no `ComputeSpec` field and no opt-out — and it is a
Hetzner-only concern, contained entirely in the Hetzner provider (no interface or schema change).

## The invariant

- Placement is **per scope**: each `HetznerCompute` (one per region/slug, plus the region-less
  global slice) owns its own groups via `ensurePlacementGroup`, memoized in `h.placementGroups` keyed
  by group index. A spread group only spreads within a Hetzner location, and each scope maps to one
  location, so cross-scope grouping would be meaningless.
- Groups are **bin-packed under Hetzner's hard cap of `maxServersPerSpreadGroup` (10) servers per
  spread group**: `Create` advances a scope-wide `serverOrdinal` and assigns the Nth server (1-based)
  to group `(serverOrdinal-1) / 10`. Group `k` is created lazily on first use as
  `naming.Resource(env, slug, "pg", fmt.Sprintf("%02d", k+1))` → `wardnet-<env>-<slug>-pg-<NN>`
  (region-less `wardnet-<env>-pg-<NN>` for the global scope).
- Assignment is **deterministic in server-creation order**, so a re-`up` keeps each server in the
  same group and does not churn.

## Caveat

Because bin-packing keys on the running ordinal, inserting a compute spec that sorts *earlier* shifts
every later server's ordinal. If that shift crosses a 10-boundary, some servers move to a different
group — and `placement_group_id` is a server input, so that can replace them. This only bites past 10
servers in one scope; below that there is a single group and no reshuffle. Do not switch to a
hash-based assignment without weighing the same trade-off (hashing avoids ordinal shift but can't
guarantee an even ≤10 fill).

## Applies to

`providers/hetzner/compute.go` (`Create` sets `PlacementGroupId`; `ensurePlacementGroup`;
`serverOrdinal`; `maxServersPerSpreadGroup`). A future compute provider is free to omit placement
groups — this is a Hetzner reliability optimization, not a cross-provider contract. Test mocks that
drive `Create` must return a **numeric** ID for `hcloud:index/placementGroup:PlacementGroup` (like
firewalls) because `Create` parses it to an int via `idToInt`.

## Why

Spread placement groups are free and have no downside for our single-instance service hosts, so there
is no reason to make them opt-in — always-on is simpler and strictly more reliable. Keeping the grain
per-scope and the fill deterministic makes the assignment stable across deploys, which matters because
moving a server between groups can force a replacement.
