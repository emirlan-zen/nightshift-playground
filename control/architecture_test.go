package main

// Architecture fitness test (ADR-0020), patterned on the example-repo backend. It locks
// the dependency direction of the scheduling domain slice extracted from the
// internal/control grab-bag: the kernel (internal/run) and the launch-queue
// decisions (internal/queue) are pure domain logic and must not reach back into
// the HTTP transport or the control composition root. If a later change adds
// such an import — even transitively — this test fails, so the direction can't
// silently rot.

import (
	"testing"

	"nightshift/control/internal/archtest"
)

func TestArchitectureDomainStaysPure(t *testing.T) {
	const (
		httpLayer = "net/http"
		control   = "nightshift/control/internal/control"
		queue     = "nightshift/control/internal/queue"
		run       = "nightshift/control/internal/run"
		scheduler = "nightshift/control/internal/scheduler"
	)

	// The domain packages must not depend on the transport layer or the
	// composition root — dependencies point inward.
	archtest.Package(t, run).MustNotImport(httpLayer, control, queue, scheduler)
	archtest.Package(t, queue).MustNotImport(httpLayer, control, scheduler)

	// The kernel is the innermost ring: queue may depend on run, never the
	// reverse.
	archtest.Package(t, run).MustNotImport(queue)

	// The launch application service (ADR-0020) orchestrates the pure decisions
	// through ports. It composes the domain rings (run + queue) but must never
	// reach back out to the transport layer or the control composition root — the
	// production adapters that bind its Clock/Store/Launcher ports live in
	// internal/control, and dependencies point inward.
	archtest.Package(t, scheduler).MustNotImport(httpLayer, control)
}
