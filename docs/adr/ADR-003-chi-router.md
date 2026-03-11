# ADR-003: Chi router

**Date:** 2026-03-12
**Status:** accepted

## Context

The project requires an HTTP router that supports route groups (for applying JWT middleware selectively), URL parameters, and clean integration with standard `net/http` middleware.

## Decision

Use `github.com/go-chi/chi/v5`.

## Rationale

- **Standard `net/http` compatibility** — chi handlers and middleware are plain `http.Handler` / `func(http.Handler) http.Handler`. This avoids framework lock-in and makes unit testing straightforward with `httptest.NewRecorder`.
- **Route groups** (`r.Group`, `r.Route`) make it trivial to apply `JWTMiddleware` to protected routes while keeping `/health`, `/auth/token`, and `/metrics` public.
- **No reflection or code generation** — chi routes compile-time safe through standard function types.
- Alternatives considered:
  - **Gin** — slightly faster benchmarks but uses a custom context type that breaks compatibility with middleware written for `net/http`.
  - **stdlib `net/http`** — no route parameter support, no middleware groups; would require significant boilerplate.
  - **Echo** — similar to chi but uses a custom binder that adds unnecessary complexity for a JSON-only API.

## Consequences

- The `middleware.RequestID` built-in only sets the ID in the request context; a custom `responseRequestID` middleware was added to copy it into the `X-Request-Id` response header.
- chi does not provide built-in structured logging; that can be added later via `slog-chi` if needed.
