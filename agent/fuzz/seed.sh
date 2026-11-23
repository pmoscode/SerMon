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
# redaction: the same messages without rules, and with a rule that has a secret group.
mkdir -p corpus/redaction
for file in corpus/security_parser/fixture*; do
  name=$(basename "$file")
  { printf '\000'; tail -c +2 "$file"; } >"corpus/redaction/$name"
  { printf '\001'; printf 'pin=(?P<secret>[0-9]+)\000'; tail -c +2 "$file"; } >"corpus/redaction/rule$name"
done
# multiline: the multi-line fixtures, by indentation and by a start byte ('2' of the dates).
mkdir -p corpus/multiline
for file in ../fixtures/logs/multiline/*.log; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\000'; cat "$file"; } >"corpus/multiline/indent$name"
  { printf '2'; cat "$file"; } >"corpus/multiline/start$name"
done
# process_parsers: the files of the process fixtures.
mkdir -p corpus/process_parsers
for file in ../fixtures/processes/*/proc/*/stat ../fixtures/processes/*/proc/*/status ../fixtures/processes/*/proc/*/cgroup ../fixtures/processes/*/etc/passwd; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  cp "$file" "corpus/process_parsers/fixture$name"
done
