// Package sizes holds the set of valid compute size names (the "size table") —
// cloud-agnostic vocabulary with no cpus/memory payload. Built-in defaults live
// here; a project may replace them per environment with resources/<env>/sizes.yaml
// (see internal/loader). A compute spec declares only a size name; each provider
// maps that name to a concrete SKU via its region realization (see the provider
// packages).
package sizes

import "fmt"

// Table is the set of valid compute size names (e.g. "SMALL").
type Table map[string]struct{}

// DefaultTable returns a fresh copy of the built-in size table. A per-env
// sizes.yaml, when present, replaces this wholesale rather than merging.
func DefaultTable() Table {
	return Table{
		"SMALL":  {},
		"MEDIUM": {},
		"LARGE":  {},
	}
}

// Resolve reports whether a size name is defined in the table, returning an
// error if it is not.
func (t Table) Resolve(name string) error {
	if _, ok := t[name]; !ok {
		return fmt.Errorf("unknown compute size %q — define it in resources/<env>/sizes.yaml or internal/sizes", name)
	}
	return nil
}
