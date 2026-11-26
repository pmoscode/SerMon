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
# helper_request: a switch byte and one request of each operation (protobuf by hand, ADR 0035).
mkdir -p corpus/helper_request
python3 - <<'PY'
def field(n, wire, payload):
    key = bytes([n << 3 | wire])
    return key + (bytes([len(payload)]) + payload if wire == 2 else payload)
run_id = b"0193f5a0-0000-7000-8000-000000000001"
ops = {
    "get_status": field(1, 2, b""),
    "list_updates": field(2, 2, field(1, 0, b"\x01")),
    "apply_updates": field(3, 2, field(1, 2, run_id) + field(2, 0, b"\x01")),
    "reboot": field(4, 2, field(1, 2, run_id) + field(2, 2, b"kernel 6.1.0-28")),
    "self_update": field(5, 2, field(1, 2, run_id)),
    "run_result": field(6, 2, field(1, 2, run_id)),
    "read_smart": field(8, 2, b""),
}
for name, body in ops.items():
    open(f"corpus/helper_request/{name}", "wb").write(b"\x1f" + body)
PY
# update_parsers: the captured outputs of apt, dnf and apk (M4-06).
mkdir -p corpus/update_parsers
for file in ../fixtures/updates/*/*.txt; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  cp "$file" "corpus/update_parsers/fixture$name"
done
# module_parsers: a parser byte and the recorded files (0 /proc/net/tcp*, 1 /proc/net/unix).
mkdir -p corpus/module_parsers
for file in ../fixtures/discovery/*/proc/net/tcp*; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\000'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
for file in ../fixtures/discovery/*/proc/net/unix; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\001'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
# 2: certificates, 3: a URL and a redirect target.
printf '\003http://localhost:8080/nginx_status?auto/server-status' >corpus/module_parsers/url
printf '\003https://[::1]:8443/a/b//other/x?y' >corpus/module_parsers/url-v6
# smart_parser: the outputs of smartctl (M5-18).
mkdir -p corpus/smart_parser
for file in ../fixtures/modules/smart/*/*.json; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  cp "$file" "corpus/smart_parser/fixture$name"
done
# 2 again: the DER of the TLS fixtures (M5-09).
for file in ../fixtures/modules/tls/certs/*.crt; do
  name=$(basename "$file" .crt)
  python3 -c 'import base64,sys; t=open(sys.argv[1]).read().split("-----")[2]; sys.stdout.buffer.write(b"\002"+base64.b64decode(t))' "$file" >"corpus/module_parsers/cert-$name"
done
# cgroup_parsers: the cgroup files and process files of the module docker (M5-16).
mkdir -p corpus/cgroup_parsers
find ../fixtures/modules/docker -type f ! -name '*.sh' ! -name '*.md' | while IFS= read -r file; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  cp "$file" "corpus/cgroup_parsers/fixture$name"
done
# 4: systemctl show of the module systemd_units (M5-10).
for file in ../fixtures/modules/systemd_units/*/show.txt; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\004'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
# 5: the stub_status pages of nginx (M5-11).
for file in ../fixtures/modules/nginx/*/stub_status.txt; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\005'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
