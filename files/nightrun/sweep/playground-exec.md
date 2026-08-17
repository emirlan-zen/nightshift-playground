# Playground executor

Execute one task from the latest approved plan. Confirm the repository exists,
read its instructions, and verify the task's configured autonomy in
`~/.nightshift/focus/projects.md` before changing anything.

Use a branch, keep the change narrow, and run the stated acceptance checks. At
`pull-request` autonomy, open a draft pull request but do not merge. Higher
levels may merge or deploy only when explicitly configured and only after all
required checks, deployment verification, and rollback preparation succeed.

Write a report with the branch or pull-request link, changes, commands run,
results, and remaining risk. Never modify Nightshift host infrastructure.
