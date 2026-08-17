// Package archtest is a small fluent import-boundary checker, patterned on the
// example-repo architecture fitness test. It locks the dependency direction of the
// control plane as an executable assertion: a domain package that must not
// depend on the transport layer or the composition root fails the build test
// the moment someone adds the wrong import.
//
// It computes a package's FULL TRANSITIVE dependency set via `go list -deps`
// (the toolchain is present wherever tests run — CI and the box), so a boundary
// violation is caught even when it hides behind an intermediate package. This
// keeps the check dependency-free: no golang.org/x/tools in go.mod, which
// matters because the box builds the binary with GOTOOLCHAIN=local.
package archtest

import (
	"os/exec"
	"strings"
	"testing"
)

// Boundary is a package under an import constraint. Build one with Package, then
// chain MustNotImport assertions.
type Boundary struct {
	t    testing.TB
	pkg  string
	deps map[string]bool
}

// Package resolves pkg's transitive dependency set once for the assertions that
// follow. A resolution failure fails the test — an architecture check that
// silently skips is worse than none.
func Package(t testing.TB, pkg string) Boundary {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	if err != nil {
		t.Fatalf("archtest: go list -deps %s: %v\n%s", pkg, err, out)
	}
	deps := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			deps[line] = true
		}
	}
	return Boundary{t: t, pkg: pkg, deps: deps}
}

// MustNotImport asserts pkg has none of the forbidden packages anywhere in its
// transitive import graph. Exact import-path matching (not substring), so
// "internal/control" never trips on the stdlib's "internal/runtime".
func (b Boundary) MustNotImport(forbidden ...string) Boundary {
	b.t.Helper()
	for _, f := range forbidden {
		if b.deps[f] {
			b.t.Errorf("architecture violation: %s must not import %s (found in its transitive deps)", b.pkg, f)
		}
	}
	return b
}
