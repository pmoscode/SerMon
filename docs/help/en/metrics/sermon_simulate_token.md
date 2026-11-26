# Simulated credential arrived

1 if the test module `simulate` got its credential `token`, else 0. It never reports the value itself.

## Reading it

This checks that a module's credentials reach the agent. The module exists only in builds with the feature
`simulate` (`just simulate`), never in a release.

Metric: `sermon_simulate_token`
