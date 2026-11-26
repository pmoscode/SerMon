# Simulated value

The value the test module `simulate` reports as configured (field "Value").

## Reading it

The module exists only in builds with the feature `simulate` (`just simulate`), never in a release. It reaches
nothing on the host and serves to try modules, alerts and the UI without a real service.

Metric: `sermon_simulate_value`
