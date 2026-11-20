# Pressure stall information (PSI)

Share of the time tasks had to wait for a resource (label `resource`: cpu, memory, io; `kind`: some = some tasks, full = all).

## Reading it

PSI shows bottlenecks more directly than usage: a few percent of `full` for memory or io are noticeable.

Metric: `sermon_pressure_stall_ratio`
