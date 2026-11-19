# CPU busy

The share of time the processors were not idle – averaged over all cores.

## Reading it

- Constantly above 80 % means the server has hardly any headroom; more load leads to waiting.
- Short spikes up to 100 % are normal, for example during backups, updates or builds.
- A high value isn't always computing: **iowait** (waiting for disks) and **steal** (time the hypervisor gives to another VM) count here too.

## Next steps

1. Look at the split into `user`, `system`, `iowait` and `steal` in the node detail.
2. Find the processes with the highest CPU usage, e.g. with `top`.
3. With high `steal`, check with your hosting provider or the load of the hypervisor.
