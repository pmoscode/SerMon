# Agent pending batches

Data batches in the agent's buffer the server hasn't acknowledged yet.

## Reading it

If it rises, the agent can't reach the server; the data is delivered later.

Metric: `sermon_agent_buffer_pending_batches`
