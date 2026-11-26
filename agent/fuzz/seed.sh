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
  cp -f "$file" "corpus/cgroup_parsers/fixture$name"
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
# 6: server-status?auto of Apache (M5-12).
for file in ../fixtures/modules/apache/*/server-status.txt; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\006'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
# 7: INFO of Redis and Valkey (M5-15).
for file in ../fixtures/modules/redis/*/info-*.txt; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\007'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
# postgres_wire: answers as the server frames them, from the recorded result sets (M5-13).
mkdir -p corpus/postgres_wire
python3 - <<'PY'
import json, glob, struct, os
def msg(tag, body):
    return tag + struct.pack(">I", len(body) + 4) + body
for f in glob.glob("../fixtures/modules/postgres/*/*.json"):
    out = b""
    for s in json.load(open(f)):
        cols = s["columns"]
        out += msg(b"T", struct.pack(">h", len(cols)) + b"".join(c.encode() + b"\0" + bytes(18) for c in cols))
        for r in s["rows"]:
            body = struct.pack(">h", len(r))
            for v in r:
                body += struct.pack(">i", -1) if v is None else struct.pack(">i", len(v.encode())) + v.encode()
            out += msg(b"D", body)
        out += msg(b"C", b"SELECT\0")
    out += msg(b"Z", b"I")
    name = f.replace("/", "_").replace(".", "_")
    open(os.path.join("corpus/postgres_wire", "fixture" + name), "wb").write(out)
open("corpus/postgres_wire/scram", "wb").write(b"r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096")
PY
# 8: /metrics pages for the Prometheus scraper (M5-19).
for file in ../fixtures/modules/prometheus/*/metrics-*.txt; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\010'; head -c 60000 "$file"; } >"corpus/module_parsers/fixture$name"
done
# mysql_wire: the recorded handshakes, and the result sets as the server frames them (M5-14).
mkdir -p corpus/mysql_wire
python3 - <<'PY'
import json, glob, os
def packet(seq, body):
    return len(body).to_bytes(3, "little") + bytes([seq]) + body
def lenenc(b):
    return bytes([0xfb]) if b is None else bytes([len(b)]) + b if len(b) < 251 else b"\xfc" + len(b).to_bytes(2, "little") + b
for f in glob.glob("../fixtures/modules/mysql/*/handshake.bin"):
    name = f.replace("/", "_").replace(".", "_")
    open(os.path.join("corpus/mysql_wire", "fixture" + name), "wb").write(open(f, "rb").read())
for f in glob.glob("../fixtures/modules/mysql/*/*.json"):
    s = json.load(open(f))
    out, seq = packet(1, bytes([len(s["columns"])])), 2
    for c in s["columns"]:
        out += packet(seq, b"".join(lenenc(x) for x in [b"def", b"", b"", b"", c.encode(), c.encode()]) + bytes(12)); seq += 1
    out += packet(seq, b"\xfe\0\0\x02\0"); seq += 1
    for r in s["rows"]:
        out += packet(seq % 256, b"".join(lenenc(None if v is None else v.encode()) for v in r)); seq += 1
    out += packet(seq % 256, b"\xfe\0\0\x02\0")
    name = f.replace("/", "_").replace(".", "_")
    open(os.path.join("corpus/mysql_wire", "fixture" + name), "wb").write(out)
PY
# 9: answers of the Docker API (M5-17).
for file in ../fixtures/modules/docker_socket/*/*.json; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\011'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
for file in ../fixtures/modules/docker_socket/*/logs-*.bin; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\011'; cat "$file"; } >"corpus/module_parsers/fixture$name"
done
# db_logins: a switch byte (bit 6: MySQL, bit 7: a secure channel, PostgreSQL's auth by % 3) and
# what a server sends during the login (M5-33).
mkdir -p corpus/db_logins
python3 - <<'PY'
import struct
def msg(tag, body):
    return tag + struct.pack(">I", len(body) + 4) + body
def auth(method, data=b""):
    return msg(b"R", struct.pack(">i", method) + data)
ready = auth(0) + msg(b"S", b"server_version\x0017.2\0") + msg(b"K", bytes(8)) + msg(b"Z", b"I")
first = b"r=rOprNGfwEbeRWgbNEkqOsrv,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
seeds = {
    "pg_scram": b"\x00" + auth(10, b"SCRAM-SHA-256\0\0") + auth(11, first) + auth(12, b"v=AAAA") + ready,
    "pg_scram_skip": b"\x00" + auth(10, b"SCRAM-SHA-256\0\0") + auth(11, first) + ready,
    "pg_md5": b"\x01" + auth(5, b"\x01\x02\x03\x04") + ready,
    "pg_password": b"\x82" + auth(3) + ready,
    "pg_trust": b"\x00" + ready,
}
def packet(seq, body):
    return len(body).to_bytes(3, "little") + bytes([seq]) + body
ok = b"\x00\x00\x00\x02\x00\x00\x00"
seeds["my_fast"] = b"\x40" + packet(2, b"\x01\x03") + packet(3, ok)
seeds["my_full"] = b"\xc0" + packet(2, b"\x01\x04") + packet(4, ok)
seeds["my_switch"] = b"\x40" + packet(2, b"\xfemysql_native_password\0" + bytes(range(1, 21)) + b"\0") + packet(4, ok)
for name, data in seeds.items():
    open("corpus/db_logins/" + name, "wb").write(data)
PY
# module_clients (M5-33): 0 a Redis reply (INFO as a bulk string), 1 four bytes of time and a PEM
# chain, 2 a container's output (bit 7: TTY), 3 a name pattern, NUL, a name.
mkdir -p corpus/module_clients
for file in ../fixtures/modules/redis/*/info-*.txt; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\000$%s\r\n' "$(wc -c <"$file" | tr -d ' ')"; cat "$file"; printf '\r\n'; } >"corpus/module_clients/info$name"
done
for file in ../fixtures/modules/tls/certs/*.crt ../fixtures/modules/tls/certs/*.pem; do
  [ -f "$file" ] || continue
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\001\152\000\000\000'; cat "$file"; } >"corpus/module_clients/pem$name"
done
for file in ../fixtures/modules/docker_socket/*/logs-talker.bin; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\002'; cat "$file"; } >"corpus/module_clients/logs$name"
done
for file in ../fixtures/modules/docker_socket/*/logs-tty.bin; do
  name=$(printf '%s' "$file" | tr '/.' '__')
  { printf '\202'; cat "$file"; } >"corpus/module_clients/tty$name"
done
printf '\003app-*-db\000app-1-db' >corpus/module_clients/pattern
