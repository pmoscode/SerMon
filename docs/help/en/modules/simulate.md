# Simulation

Test module for development and demos: it reaches nothing on the host and behaves as configured. The module exists only
in builds with the feature `simulate` (`just simulate`), never in a release.

## Settings

- **Behavior:** `ok` reports the metrics; `fail` reports an error (state `error`), `hang` hangs longer than any time
  budget (`timeout`), `panic` crashes (`crashed`) and `flood` sends more values than a run may (`error`). This shows
  that the runtime contains a broken module run and the UI shows it correctly.
- **Value:** the value of `sermon_simulate_value`.
- **Token:** a credential to try things out; the module only tells whether it arrived (`sermon_simulate_token`), never
  its content.

## Metrics

- [Simulated value](help:metrics/sermon_simulate_value)
- [Simulated credential arrived](help:metrics/sermon_simulate_token)

## Suggestion

Virtual nodes from `just simulate` pretend to run a process `sermon-simulate`; the agent then suggests the module as it
would for a real service.
