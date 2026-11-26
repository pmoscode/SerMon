# Gaps in container logs

How many of the containers under **Container logs** lost lines in this run: the answer held the full 1,000 lines
(Docker cut off older ones), or the output couldn't be read even with 100 lines (too large, an error) – then the
module jumps to the run's time.

## Reading it

From the module `docker_socket`, only with container logs. Above 0 for long means a container writes more than the
limits allow (312 lines per run, half of the node's log rate). Remedy: shorten the module's interval, make the
container quieter or take it out of the container logs.

Metric: `sermon_docker_socket_log_gaps`
