# Security Policy

Security is SerMon's top priority. Thank you for helping keep SerMon and its users safe.

## Supported versions

SerMon is in early development. There is no released version yet; security fixes are applied to `main`.
Once v1.0 is released, the latest minor version and the previous minor version will receive security fixes.

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues, discussions or pull requests.**

Report them privately via GitHub's **Private Vulnerability Reporting**:
repository → **Security** tab → **Report a vulnerability**.

Please include as much of the following as you can:

- affected component (agent, root helper, server, web UI, install script, packaging) and version/commit
- type of issue (e.g. tenant isolation bypass, privilege escalation, RCE, XSS, SSRF, auth bypass)
- step-by-step reproduction, proof of concept, and impact assessment
- any suggested mitigation

## What to expect

| Step | Target |
|---|---|
| Acknowledgement of your report | within 3 business days |
| Initial assessment and severity rating | within 10 business days |
| Fix or mitigation for critical/high issues | as fast as possible, typically within 30 days |
| Coordinated public disclosure | after a fix is available, at the latest 90 days after the report unless agreed otherwise |

We will keep you informed about progress and credit you in the advisory unless you prefer to stay anonymous.

## Scope

In scope: all code in this repository – agent (`agent/`), root helper, server (`server/`), web UI (`web/`),
protocol definitions (`proto/`), install script and packages (`packaging/`), and deployment files (`deploy/`).

Out of scope: vulnerabilities in third-party dependencies that are already publicly known (please report those
upstream; we track them via automated scanning), and findings that require a fully compromised host or server
administrator account without crossing a documented security boundary.

The security boundaries, assets and threats are described in the threat model
([`docs/security/threat-model.md`](docs/security/threat-model.md), German).

## Safe harbor

We will not pursue legal action against researchers who act in good faith: who avoid privacy violations, data
destruction and service disruption, only test against their own installations, and give us reasonable time to
fix the issue before disclosure.
