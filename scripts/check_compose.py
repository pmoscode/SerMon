"""Rule checks for a docker compose config (JSON from `docker compose config --format json`).

Called by check-compose.sh: python3 -I check_compose.py <loopback_only:true|false> <compose-file> < config.json
"""

import json
import os
import re
import sys


def check(services: dict, base: str, loopback_only: bool) -> list[str]:
    errors = []
    for name, svc in services.items():
        image = svc.get("image", "")
        if "@sha256:" not in image:
            errors.append(f"{name}: image {image!r} is not pinned by digest")

        if loopback_only:
            for port in svc.get("ports", []):
                host_ip = port.get("host_ip") if isinstance(port, dict) else str(port).rsplit(":", 2)[0]
                if host_ip != "127.0.0.1":
                    errors.append(f"{name}: port {port} is not bound to 127.0.0.1")

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
    compose_file = sys.argv[2]
    services = json.load(sys.stdin).get("services", {})
    errors = check(services, os.path.dirname(os.path.abspath(compose_file)), loopback_only)
    for error in errors:
        print(f"✗ {error}", file=sys.stderr)
    if errors:
        return 1
    print(f"✓ {compose_file}: {len(services)} services ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
