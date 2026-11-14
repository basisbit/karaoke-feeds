# Security policy

## Reporting a vulnerability

Please report security problems **privately**, not in a public issue:
use **"Report a vulnerability"** on the Security tab of this repository (GitHub private vulnerability reporting).

Please include what is affected (kfeed command, part of the format, a web server configuration), how to reproduce
it, and what an attacker could do with it. You'll get an answer within about a week. This is a volunteer project,
so fixes may take longer; we'll agree with you on when to publish details. There is no bug bounty.

## What is in scope

- `kfeed` (all commands), in the latest release and on `main`
- the security rules of the format: signatures, key rotation, rollback protection, path and file name rules (SPEC.md §5 and §6)
- the web server configurations in `hosting/`

Out of scope: the content of third-party feeds (report it to the feed's operator; every feed names a contact),
and vulnerabilities in web servers, games or CDNs themselves (report them to their projects).

## Supported versions

Only the latest release gets fixes. The format is a draft before version 1.0 and may change for security reasons.
