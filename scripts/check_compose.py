"""Rule checks for a docker compose config (JSON from `docker compose config --format json`).

Called by check-compose.sh:
  python3 -I check_compose.py <loopback_only:true|false> <hardened:true|false> <compose-file> < config.json
"""

import json
import os
import re
import sys


def unpinned_bases(build: dict) -> list[str]:
    """Base images of a Dockerfile that are not pinned by digest (build args resolved)."""
    path = os.path.normpath(os.path.join(build.get("context", "."), build.get("dockerfile", "Dockerfile")))
    if not os.path.isfile(path):
        return [f"missing Dockerfile {path}"]
    args = {k: v for k, v in (build.get("args") or {}).items() if v is not None}
    stages, bad = set(), []
    for line in open(path, encoding="utf-8"):
        words = line.split()
        if not words:
            continue
        keyword = words[0].upper()
        if keyword == "ARG" and len(words) > 1 and "=" in words[1]:
            key, default = words[1].split("=", 1)
            args.setdefault(key, default)
        elif keyword == "FROM":
            ref = next((w for w in words[1:] if not w.startswith("--")), "")
            image = re.sub(r"\$\{?(\w+)\}?", lambda m: args.get(m.group(1), ""), ref)
            if image not in stages and image != "scratch" and "@sha256:" not in image:
                bad.append(image or ref)
            if len(words) >= 4 and words[-2].upper() == "AS":
                stages.add(words[-1])
    return bad


def unhardened(svc: dict) -> list[str]:
    """What keeps a production container from being locked down (M2-26)."""
    problems = []
    if svc.get("read_only") is not True:
        problems.append("root file system is writable (read_only: true)")
    if "ALL" not in [c.upper() for c in svc.get("cap_drop") or []]:
        problems.append("capabilities are not dropped (cap_drop: [ALL])")
    if not any(re.fullmatch(r"no-new-privileges(:true)?", o) for o in svc.get("security_opt") or []):
        problems.append("new privileges are allowed (security_opt: no-new-privileges:true)")
    if svc.get("privileged"):
        problems.append("runs privileged")
    if svc.get("network_mode") == "host":
        problems.append("uses the host network")
    for volume in svc.get("volumes") or []:
        if str(volume.get("source", "")).endswith("docker.sock"):
            problems.append("mounts the Docker socket")
    return problems


def check(services: dict, base: str, loopback_only: bool, hardened: bool = False) -> list[str]:
    errors = []
    for name, svc in services.items():
        image = svc.get("image", "")
        if "build" in svc:
            # Built locally: every base image of the Dockerfile must be pinned.
            for bad in unpinned_bases(svc["build"]):
                errors.append(f"{name}: base image {bad!r} is not pinned by digest")
        elif "@sha256:" not in image:
            errors.append(f"{name}: image {image!r} is not pinned by digest")

        if loopback_only:
            for port in svc.get("ports", []):
                host_ip = port.get("host_ip") if isinstance(port, dict) else str(port).rsplit(":", 2)[0]
                if host_ip != "127.0.0.1":
                    errors.append(f"{name}: port {port} is not bound to 127.0.0.1")

        if hardened:
            errors.extend(f"{name}: {problem}" for problem in unhardened(svc))

        if image.startswith("timescale/timescaledb"):
            if str(svc.get("environment", {}).get("TIMESCALEDB_TELEMETRY", "")).lower() != "off":
                errors.append(f'{name}: TIMESCALEDB_TELEMETRY must be "off"')

        if image.startswith("grafana/loki"):
            args = svc.get("command", [])
            target = next((a.split("=", 1)[1] for a in args if a.startswith("-config.file=")), None)
            source = next((v.get("source") for v in svc.get("volumes", []) if v.get("target") == target), None)
            path = os.path.join(base, source) if source else ""
            content = open(path, encoding="utf-8").read() if path and os.path.isfile(path) else ""
            if not re.search(r"^\s*reporting_enabled:\s*false\b", content, re.MULTILINE):
                errors.append(f"{name}: Loki config must set analytics.reporting_enabled: false")
    return errors


def main() -> int:
    loopback_only = sys.argv[1] == "true"
    hardened = sys.argv[2] == "true"
    compose_file = sys.argv[3]
    services = json.load(sys.stdin).get("services", {})
    errors = check(services, os.path.dirname(os.path.abspath(compose_file)), loopback_only, hardened)
    for error in errors:
        print(f"✗ {error}", file=sys.stderr)
    if errors:
        return 1
    print(f"✓ {compose_file}: {len(services)} services ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
