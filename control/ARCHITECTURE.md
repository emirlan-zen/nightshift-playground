# Control-plane architecture

The control plane is a modular monolith. It remains one statically built Go
binary with the React application embedded, while code is divided by business
capability and dependencies point inward.

## Go dependency direction

```text
main.go
  -> internal/control       composition, automation orchestration, route map
       -> internal/{session,focus,machine,ticket,access}
            -> ports expressed as small Go interfaces
       -> internal/scheduler     launch application service (ports: Clock/Store/Launcher)
            -> internal/{run,queue}   scheduling domain kernel + launch decisions
       -> internal/adapter/rc
       -> internal/platform/{config,logging}
       -> internal/transport/httpapi
```

- `main.go` only translates the process exit status.
- `internal/control` is the composition root and automation bounded context. It
  wires workers in dependency order; it does not own access verification,
  remote-session rules, focus persistence, or host-vitals caching.
- `internal/run` is the scheduling domain kernel (ADR-0020): the value types the
  launch queue reasons about — `Executor`, `Verdict`, launch `Tier`, and `Job`,
  the scheduling projection of a control-plane job. Pure, no I/O; `Job.LogAttrs`
  is the ONE lifecycle log schema — the stable top-level `run_id`/`node`/`agent`/
  `executor` every scheduler transition carries, so a night is reconstructable
  from the journal by a single `run_id` filter. It is the innermost ring: it
  imports nothing internal.
- `internal/queue` holds the launch-admission DECISIONS (ADR-0019): concurrency
  caps, executor-health holds, the launch-time deadline re-clamp, starvation,
  and the dead-unit watchdog — pure functions over `run.Job` and immutable
  snapshots (`Load`, `Health`). No I/O; the decisions are table-tested in isolation.
- `internal/scheduler` is the launch **application service** (ADR-0020): it owns
  the admit → persist → launch lifecycle for one queued run and reaches the box
  only through three narrow ports — `Clock`, `Store`, `Launcher`. It composes the
  domain rings (imports `run` + `queue`) and orchestrates their decisions, but it
  never touches the filesystem, sudo, HTTP, or the composition root, so the whole
  sequence is table-tested with fakes. It also exports the one lifecycle emitter
  (`Log`) both it and the adapter use, so the scheduler speaks one query schema.
- `internal/control`'s `queue.go`/`nightrun.go` are the **adapters**: they gather
  the box's live state into the immutable snapshots (`Load`, `Health`), bind the
  service's ports (`Store` → job files, `Launcher` → `nightshift-rc`, `Clock` →
  the tick's wall time), drive the per-tick candidate loop, and apply the
  observability side effects (starvation, hold dedup, dep-skip markers).
- `architecture_test.go` (module root) + `internal/archtest` lock this
  direction as an executable fitness test (patterned on example-repo): `run`, `queue`, and
  `scheduler` must not import `net/http` or `internal/control`, even transitively;
  the kernel must not import the queue; and the domain rings must not import the
  application service. A wrong import fails the build test.
- `internal/session` owns remote-control session lifecycle and validates every
  agent/instance before crossing the privileged port.
- `internal/ticket` owns the ticket aggregate, transitions, validation, and the
  sweep projection. JSON/filesystem persistence remains compatible with the
  independently running `nightshift-ticket` CLI.
- `internal/focus` owns the allowlisted north-star documents and atomic writes.
- `internal/machine` owns privilege-free machine vitals and caching.
- `internal/access` verifies Cloudflare identity and fails closed.
- `internal/adapter` and `internal/platform` contain replaceable infrastructure.

New behavior belongs in the capability that owns its language and invariants.
Cross-capability work goes through an interface or an immutable value; it must
not reach into another capability's storage layout.

## Web dependency direction

Feature pages and their typed endpoint clients live together under
`web/src/features/<feature>`. Shared request mechanics live in
`web/src/shared/api`. `web/src/lib/api.ts` is a compatibility barrel, so the
split did not require a risky all-at-once change to every view and test.

## Logging and recovery

Production emits JSON `slog` events to stdout. systemd captures stdout/stderr in
journald, which supplies persistence, timestamps, boot boundaries, filtering,
and rotation. Every HTTP request has an `X-Request-ID` and a completion event;
mutations and important lifecycle transitions also emit stable event names such
as `session.started`, `ticket.updated`, `flow.created`, and `run.started`. The
launch queue emits its state transitions the same way, each carrying the run's
domain identity (run id, node, agent, executor) so a night is reconstructable
from the journal by run id: `run.started` / `run.start_failed`, `run.skipped`
(a window that closed before launch or a dead upstream), `run.watchdog_released`
(a dead unit's cap slot freed), and `queue.held` / `queue.resumed` (executor
health pausing and resuming launches).

Do not log prompts, ticket bodies, report text, tokens, headers, or secrets.
Log identifiers, state transitions, durations, counts, and wrapped errors.

Useful production queries:

```bash
journalctl -u nightshift-control --since today -o json-pretty
journalctl -u nightshift-control -g 'run.start_failed|flow.mint_failed' --since '24 hours ago'
journalctl -u nightshift-control --boot -g '"level":"(WARN|ERROR)"'
```

The durable recovery sources remain `~/.nightshift/` and
`~/.nightshift/observability.db`; logs explain what happened but are not used as
application state. systemd restarts the service, the scheduler reloads persisted
jobs, and already-started runs remain independent transient units.
