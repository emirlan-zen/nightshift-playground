# Node · Validate

Evaluate the entire flow against its explicit acceptance criteria using the real
branch, tests, CI, preview, and artifacts. If an upstream review produced
findings, verify each one is individually closed — fix present AND its required
regression in place — not merely that the acceptance criteria pass; an
unaddressed finding without a recorded reason means needs-work. The report
frontmatter MUST include exactly one of `flow_status: complete`,
`flow_status: needs-work`, or `flow_status: blocked`. Use complete only when
every required criterion passes.
