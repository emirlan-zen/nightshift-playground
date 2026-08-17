# Night-run contract

Unattended runs operate only inside `~/workspace/playground` and only on
repositories the operator has placed there.

## Always allowed

- Read code, documentation, issues, and local Nightshift state.
- Run tests, linters, builds, and read-only diagnostics.
- Create a branch, make reversible edits, commit, push the branch, and open a
  draft pull request when credentials and repository policy permit it.
- Write reports, tickets, plans, briefs, and project state under
  `~/.nightshift`.

## Requires explicit project authorization

- Merging a pull request.
- Releasing or deploying.
- Mutating cloud, DNS, databases, production systems, or external services.
- Closing issues or tickets, sending messages, or changing repository settings.

Project authorization is recorded in `~/.nightshift/focus/projects.md` using one
of these levels:

- `review-only`: analyze and report; no code changes.
- `pull-request`: branch, test, push, and open a draft pull request.
- `merge`: the pull-request level plus merge after required tests and checks.
- `deploy`: the merge level plus deploy and verify using the project's documented
  rollback procedure.

When authorization is missing or ambiguous, choose the less powerful level and
report the blocked action.

## Absolute boundaries

- Never read or change `/etc/nightshift/secrets`, authentication stores, the
  Nightshift host installation, its Terraform state, proxy configuration, or
  system services.
- Never expose tokens, private source, customer data, or personal data in
  reports or logs.
- Never bypass branch protection, disable checks, force-push a protected branch,
  or weaken security to complete a task.
- A failed deployment verification must stop further rollout and follow the
  project's rollback instructions if the configured authorization permits it.

## Reporting

Every run must leave a concise report containing the result, changed paths or
links, verification performed, remaining risk, and any action the operator must
take. Claims require command output or another concrete artifact.
