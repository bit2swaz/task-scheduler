# ADR-004: JWT authentication

**Date:** 2026-03-12
**Status:** accepted

## Context

The REST API must be protected against unauthorized use. The auth mechanism must be stateless (no server-side session store) and simple enough to issue tokens from the scheduler itself without a separate identity service.

## Decision

Use HS256-signed JWTs issued by `POST /auth/token`. The caller supplies the `JWT_SECRET` as a shared secret. Tokens expire after 24 hours. All protected routes validate the token via `JWTMiddleware`.

## Rationale

- **Stateless verification** — any node can verify a token without contacting a central store, which is important in a multi-node cluster.
- **HS256** is appropriate for a service-to-service auth scheme where both parties share the secret. Asymmetric algorithms (RS256) would add key-management complexity with no additional security benefit here.
- **24-hour expiry** balances operational convenience (tokens survive restarts) with a reasonable revocation window.
- Limitations acknowledged:
  - There is no token revocation mechanism. A compromised token is valid until expiry.
  - The `JWT_SECRET` is a single shared secret across all nodes; rotation requires restarting all nodes.
  - This scheme is suitable for internal cluster communication, not for exposing the API to untrusted external clients.

## Consequences

- The `secret` field in `POST /auth/token` must be kept confidential. Do not log request bodies.
- In production, `JWT_SECRET` should be injected via a secrets manager (Vault, AWS Secrets Manager) rather than a plain environment variable.
- A future improvement is to add per-node key pairs and a JWKS endpoint, enabling zero-downtime key rotation.
