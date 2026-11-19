# Memory

The used share of the main memory, like `free`: total memory minus what programs can still get without swapping. Caches and buffers don't count, because Linux frees them when needed.

## Reading it

- Values up to about 85 % are fine.
- If it keeps rising and **swap** is used, the server slows down. When swap is full too, the kernel ends processes (OOM killer).
- A slowly but steadily rising value can point to a memory leak in a service.

## Next steps

1. Check the history and the swap usage in the node detail.
2. Find the processes with the largest memory use, e.g. with `ps aux --sort=-rss | head`.
