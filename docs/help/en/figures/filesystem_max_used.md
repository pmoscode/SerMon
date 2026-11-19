# Fullest file system

The fill level of the server's fullest file system – computed like `df`: used divided by used plus available to regular users.

## Reading it

- From about 90 % it gets tight: logs, databases and updates can fail when space runs out.
- A file system can also be full with free space left when no **inodes** are free (many small files).
- The node detail shows every file system and estimates when it runs full at the current trend.

## Next steps

1. Find the file system in the node detail.
2. Find large directories, e.g. with `du -xh --max-depth=1 / | sort -h`.
3. Clean up old logs, cached packages or backups.
