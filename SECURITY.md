# Security policy

Pamphleteer publishes notes at unguessable URLs. That is a deliberately modest
security model (see **Security model** in the [README](README.md)), so the most
useful reports are ones that show a note, attachment, file path or other vault
content being served that should not be.

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** using GitHub's private
vulnerability reporting: open the repository's **Security** tab and choose
**Report a vulnerability**. Please don't open a public issue for security
problems.

This is a small hobby project maintained in spare time. Reports are read and
handled on a best-effort basis, with no guaranteed response time.

## Supported versions

Only the latest release (the `latest` image / `main` branch) is supported.

## Scope

In scope: anything that serves content that isn't a published note or an
attachment embedded by one; ways around the sanitizer or CSP; path traversal;
symlink or hidden-directory escapes; information leaks in error responses.

Out of scope, and documented as limitations: anyone who has a note's URL can read
it (there is no login); links leaking through sharing, chat previews or proxy
logs; weak slugs chosen by hand.
