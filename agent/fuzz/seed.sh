#!/bin/sh
# Seeds the corpus of proc_parsers with the real /proc and /sys files of agent/fixtures: each seed
# is "elapsed byte, reading, NUL, the same reading" (see fuzz_targets/proc_parsers.rs).
set -eu
cd "$(dirname "$0")"
corpus=corpus/proc_parsers
mkdir -p "$corpus"
find ../fixtures -type f ! -name '*.md' | while IFS= read -r file; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  {
    printf '\024'
    cat "$file"
    printf '\000'
    cat "$file"
  } >"$corpus/fixture$name"
done
