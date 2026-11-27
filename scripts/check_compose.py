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
    # M2-39: the other ways out of a container.
    for cap in svc.get("cap_add") or []:
        if cap.upper().removeprefix("CAP_") not in SAFE_CAPS:
            problems.append(f"adds the capability {cap} (only {', '.join(sorted(SAFE_CAPS))})")
    for key in ("pid", "ipc", "userns_mode", "uts", "cgroup"):
        if svc.get(key) == "host":
            problems.append(f"shares the host's {key} namespace")
    for opt in svc.get("security_opt") or []:
        if re.fullmatch(r"(seccomp|apparmor)[:=]unconfined|label[:=]disable", opt):
            problems.append(f"turns off confinement ({opt})")
    if svc.get("devices"):
        problems.append("gets host devices")
    for volume in svc.get("volumes") or []:
        source = str(volume.get("source", ""))
        if source.endswith(".sock") or os.path.normpath(source) in HOST_RUNTIME:
            problems.append(f"mounts the host's {source} (container runtime sockets)")
    return problems


# Capabilities a hardened container may add back: switching users when it starts, binding ports
# below 1024. Everything else (SYS_ADMIN, NET_ADMIN, SYS_PTRACE, …) opens the host.
SAFE_CAPS = {"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID", "NET_BIND_SERVICE"}
# Host directories that hold the sockets of Docker and containerd.
HOST_RUNTIME = {"/run", "/var/run", "/run/containerd", "/var/run/containerd", "/run/docker", "/var/run/docker"}


def secret_names(svc: dict) -> set[str]:
    """The secrets a service gets (short or long syntax)."""
    return {s.get("source") if isinstance(s, dict) else s for s in svc.get("secrets") or []}


def misplaced_credentials(svc: dict) -> list[str]:
    """Database credentials beyond what a container needs (M2-36, T73): the schema owner, sermon_backup
    and the secret backup key only in one-shot containers, the superuser only in the database."""
    problems = []
    secrets = secret_names(svc)
    long_running = str(svc.get("restart", "no")) != "no" or svc.get("ports")
    if "database_migrate_url" in secrets and long_running:
        problems.append("keeps the schema owner's URL in a long-running container (one-shot migrate service only)")
    # V1-05: the role that reads every organization and the key that opens every backup.
    if "database_backup_url" in secrets and long_running:
        problems.append("keeps the URL of sermon_backup in a long-running container (one-shot backup service only)")
    if "backup_identity" in secrets and long_running:
        problems.append("keeps the secret backup key in a long-running container (one-shot restore service only)")
    if "postgres_password" in secrets and not svc.get("image", "").startswith(("timescale/timescaledb", "postgres")):
        problems.append("gets the database superuser's password (database container only)")
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
            errors.extend(f"{name}: {problem}" for problem in misplaced_credentials(svc))

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
            # Without it Loki ignores X-Scope-OrgID: one tenant for all organizations (ADR 0031).
            if not re.search(r"^auth_enabled:\s*true\b", content, re.MULTILINE):
                errors.append(f"{name}: Loki config must set auth_enabled: true")
            # Loki has no login of its own: only the server may reach it.
            if hardened and svc.get("ports"):
                errors.append(f"{name}: Loki must not publish ports")
            # Large queries must not take the host's memory (M3-27).
            limits = ((svc.get("deploy") or {}).get("resources") or {}).get("limits") or {}
            if hardened and not (svc.get("mem_limit") or limits.get("memory")):
                errors.append(f"{name}: Loki needs a memory limit (mem_limit)")
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
