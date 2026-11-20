#!/bin/sh
# Seeds the corpus of proc_parsers with the real /proc and /sys files of agent/fixtures: each seed
# is "elapsed byte, reading, NUL, the same reading" (see fuzz_targets/proc_parsers.rs). The
# corpus of journal_line gets every journal fixture as it is.
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
mkdir -p corpus/journal_line
for file in ../fixtures/logs/*/*.jsonl; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  cp "$file" "corpus/journal_line/fixture$name"
done
# security_parser: the messages of the journal fixtures, one file each, with a sender byte.
mkdir -p corpus/security_parser
for file in ../fixtures/logs/*/journal-security*.jsonl ../fixtures/logs/*/journal-oom.jsonl; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  python3 -c 'import json,sys; [sys.stdout.write(json.loads(l.decode("utf-8","replace")).get("MESSAGE","") + "\n") for l in open(sys.argv[1],"rb") if isinstance(json.loads(l.decode("utf-8","replace")).get("MESSAGE"), str)]' "$file" >"corpus/security_parser/messages$name.txt"
  { printf '\001'; cat "corpus/security_parser/messages$name.txt"; } >"corpus/security_parser/fixture$name"
  rm "corpus/security_parser/messages$name.txt"
done
