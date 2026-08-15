# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report privately via GitHub's private vulnerability reporting on this
repository ("Security" → "Report a vulnerability"), or e-mail
security@corelyr.com. Include a description, reproduction steps and the affected
version. You will receive an acknowledgement within 3 business days.

## Supported versions

The latest minor release receives security fixes. Older releases are fixed on a
best-effort basis.

## Verifying releases

All release artifacts are signed with Sigstore (keyless, GitHub OIDC). See the
README for `cosign verify-blob` / `cosign verify` commands and the expected
certificate identity.
