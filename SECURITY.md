# Security and private configuration

JunGo is a development preview for communication among your own paired devices. Do not use public GitHub issues to disclose secrets, private messages, real subscription URLs or unredacted deployment logs. Report vulnerabilities through the repository's private vulnerability reporting feature when it is available.

## What belongs outside Git

- Device private keys, WireGuard keys, TLS private keys and pairing payloads.
- Controller administrator tokens, registry state and local RPC endpoint credentials.
- Clash subscriptions and proxy configurations containing authentication material.
- Signing keystores, passwords, `.env` files, runtime state, inboxes, transfer records and backups.
- Screenshots or logs that reveal personal devices, infrastructure or messages.

The software creates identity material locally. Deployment examples use placeholders. The fixed values in protocol tests are synthetic test fixtures, not credentials for a deployed service; never reuse them in production. Third-party test fixtures remain under their upstream licenses.

The public repository starts from a reviewed source snapshot. Private development history, local configuration, QA inputs and release binaries are intentionally not published. `.gitignore` is only a convenience: review staged changes and run a secret scan before every push. If a real credential ever reaches a remote, revoke or rotate it; removing a line in a later commit is not sufficient.

## Distribution

Build with your own configuration and signing identity. Source archives are assembled from Git-tracked files only. A clean secret scan reduces risk but is not a guarantee against every possible disclosure. Corresponding-source and notice obligations for embedded dependencies still apply; see `LICENSE` and `THIRD_PARTY_NOTICES.md`.
