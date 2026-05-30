// Package sizes holds the compute size → {cpus, memory} mapping (the "size
// table"). Built-in defaults live here; a project may replace them per
// environment with resources/<env>/sizes.yaml (see internal/loader). A compute
// spec declares only a size name; its cpus/memory are resolved from this table.
package sizes

import "fmt"

// Size is the resource allotment for a compute size: virtual CPUs and memory
// in gibibytes.
type Size struct {
	CPUs   float64 `yaml:"cpus"`
	Memory float64 `yaml:"memory"`
}

// Table maps a size name (e.g. "SMALL") to its Size.
type Table map[string]Size

// DefaultTable returns a fresh copy of the built-in size table. A per-env
// sizes.yaml, when present, replaces this wholesale rather than merging.
func DefaultTable() Table {
	return Table{
		"SMALL":  {CPUs: 2, Memory: 4},
		"MEDIUM": {CPUs: 4, Memory: 8},
		"LARGE":  {CPUs: 8, Memory: 16},
	}
}

// Resolve returns the Size for a size name, or an error if it is not defined in
// the table.
func (t Table) Resolve(name string) (Size, error) {
	s, ok := t[name]
	if !ok {
		return Size{}, fmt.Errorf("unknown compute size %q — define it in resources/<env>/sizes.yaml or internal/sizes", name)
	}
	return s, nil
}
