// Package agent owns the configured-agent allowlist used at trust boundaries.
package agent

import "slices"

// Registry is an immutable allowlist of agent identifiers.
type Registry struct {
	all []string
}

func NewRegistry(agents []string) Registry {
	return Registry{all: slices.Clone(agents)}
}

func (r Registry) Contains(agent string) bool {
	return slices.Contains(r.all, agent)
}

func (r Registry) All() []string {
	return slices.Clone(r.all)
}
