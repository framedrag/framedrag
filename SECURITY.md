# Security Policy

framedrag is a security tool: it writes firewall policy. Bugs that would
make it fail open, block the wrong address space, or leak API keys are
security issues, not ordinary bugs. Please treat them as such.

## Supported versions

Only the latest minor release receives security fixes. (Pre-release: only
`main` is supported.)

| Version         | Supported |
|-----------------|-----------|
| latest minor    | yes       |
| everything else | no        |

## Reporting a vulnerability

**Do not open a public GitHub issue for security reports.**

Use GitHub's private vulnerability reporting:
<https://github.com/framedrag/framedrag/security/advisories/new>

Please include:

- A description of the issue and the potential impact.
- Steps to reproduce, or a minimal proof-of-concept.
- The `framedrag version` output you observed it on.
- Any mitigations you already know of.

### Response SLA

- Acknowledgement within **5 business days**.
- Triage and severity classification within **10 business days**.
- For confirmed issues, a fix timeline will be communicated in the advisory.

## Scope

In scope:

- The `framedrag` binary itself: feed fetching and TLS handling, parsers
  (they consume hostile remote input), normalization and suppression logic,
  the health/failure policy (anything that could fail open), the local HTTP
  list server, state handling, and API-key handling in the overlay config.

Out of scope (report upstream):

- The content of third-party feeds. A feed listing an address it should not
  is a matter for that feed's provider; framedrag's job is the sanity floor
  and suppression, and bypasses of *those* are in scope.
- pfSense / OPNsense vulnerabilities: report to Netgate or the OPNsense
  project respectively.
- pfBlockerNG itself: <https://github.com/pfBlockerNG/pfBlockerNG>.
- Vulnerabilities in dependencies unless they are introduced by how
  framedrag uses them.

## Coordinated disclosure

We prefer coordinated disclosure. We will credit reporters in the release
notes unless they ask to remain anonymous.

## PGP key

No PGP key is published yet. Use GitHub's private vulnerability reporting
for encrypted submission.
