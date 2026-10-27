# Temporary encrypted preflight — DO NOT MERGE

This branch replaces the registered manual deployment workflow only on its
explicit diagnostic ref. It must never be merged into main. The owner explicitly
authorized the encrypted GitHub artifact transfer of technical metadata for one
day on 2026-10-27. This does not authorize a dump,
application-data copy, deployment, image publication or application startup.
The previous local recipient private key was retired; this branch rotates only
the public recipient certificate and exact manual-ref guard.

The fixed collector selects containers by the documented Compose working
directory, verifies their project and identities, and pins Docker to the host's
local `/var/run/docker.sock`. Inherited remote Docker contexts are cleared.
It reads whitelisted metadata and small configuration fingerprints, never
configuration contents or full environments. Unknown application arguments are
redacted. PostgreSQL uses one explicit local Unix socket, a validated DB/user,
no password prompting/access, and a read-only transaction with time and lock
limits. Catalog sizes/statistics are read; no application rows are copied.

SSH retains the previously reviewed pinned host keys and strict noninteractive
configuration. The runner captures the report and diagnostics in a private
directory, encrypts the report with OpenSSL CMS AES-256-GCM/RSA-OAEP-SHA256,
then uploads **only ciphertext** for one day. The recipient certificate is
public. Its private key exists only in the owner's local private temporary
directory and is never committed, uploaded, sent to the server or printed.
Private runner files are removed even on failure. Decryption must succeed and
the report schema/status must be checked before treating inventory as complete.

Workflow success means transport/encryption succeeded; a structured partial or
failed report does not establish readiness. No command accepts arbitrary remote
commands or a data-export destination. This one-off certificate is not a
permanent inventory credential; close the PR after capture.

Local verification: eleven collector/policy tests, including real strict Go
template rendering; actionlint; encryption/decryption roundtrip. The independent
review identified inherited Docker context selection, corrected by the pinned
local socket and environment regression test.
