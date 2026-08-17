# Node · Review

Review the actual diff and artifacts with fresh context. Record each actionable
finding with severity, location, evidence, concrete fix, and regression
requirement. Do not dilute the review by doing substantive fixes. When remote CI
is already green on the exact head SHA under review, cite it instead of
re-running the full local gate suite — spend the window reading the diff; run
locally only what CI does not cover or what a specific finding makes suspect.
