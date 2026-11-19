# Network

The data received and sent per second, summed over the network interfaces of the server – without loopback and the virtual interfaces of containers and VMs (such as `veth`, `docker`, `virbr`).

## Reading it

- What is normal depends entirely on the service; jumps compared to the usual pattern stand out most.
- Unusually high outgoing traffic can point to backups, updates or a compromised service.
- When the value reaches the speed of the interface, the link is saturated.

## Next steps

1. Check in the node detail which interface is affected and look at the error and drop counters.
2. Find connections and processes with a lot of traffic, e.g. with `ss -tunap`.
