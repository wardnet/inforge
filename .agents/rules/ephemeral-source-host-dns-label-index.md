# sourceHostDNS depends on the HostFQDN env label being at index 2

`sourceHostDNS` (`cmd/inforge/ephemeral_up.go`) rewrites an ephemeral host's DNS name into its
source counterpart by swapping the label at index 2:

```go
labels := strings.Split(ephHostDNS, ".")
if len(labels) > 2 && labels[2] == identityEnv {
    labels[2] = srcEnv
}
```

This works because `naming.HostFQDN` produces:

```
<compute>.vm.<env>.<regionSlug>.<base>
 [0]     [1]  [2]     [3]       [4…]
```

where index 2 is always the environment label.

## Applies to

`cmd/inforge/ephemeral_up.go` (`sourceHostDNS`) and `internal/naming/naming.go` (`HostFQDN`).
`groupTargetsBySHA` calls `sourceHostDNS` for both service and app targets.

## Why

If `HostFQDN`'s label layout changes (e.g. inserting a new segment before `<env>`, or reordering),
`sourceHostDNS` silently looks up the wrong label, maps every ephemeral host to a non-existent
source counterpart, and skips-and-reports every workload (no delivery failures — just a silent empty
replication). Any change to `HostFQDN` must be accompanied by a matching index adjustment in
`sourceHostDNS`.
