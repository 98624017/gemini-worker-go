# Repository Guidelines

## Project Structure & Module Organization
This Go 1.22 service lives in a flat repository layout. `main.go` contains the HTTP proxy entrypoints and most request/response handling. Supporting modules sit beside it: `admin_log_ui.go` covers admin routes, `inline_data_url_cache.go` and `inline_data_url_background_fetch.go` handle image URL caching and background fetch behavior. Tests live next to the code they verify in `*_test.go` files such as `main_test.go` and `admin_log_ui_test.go`. Runtime and container assets are limited to `README.md`, `Dockerfile`, and sample output like `response.json`.

## Build, Test, and Development Commands
- `go run .` — start the service locally; set `UPSTREAM_API_KEY` first.
- `go build -o gemini-worker-go .` — build the local binary.
- `go test ./...` — run the full unit test suite.
- `go test -run TestAdminRoutes ./...` — run a focused test during iteration.
- `docker build -t gemini-worker-go .` — build the container image from `Dockerfile`.
- `docker run --rm -p 8787:8787 -e UPSTREAM_API_KEY=... gemini-worker-go` — smoke-test the container.

## Coding Style & Naming Conventions
Format all Go code with `gofmt` before submitting. Follow standard Go conventions: tabs for indentation, exported names in PascalCase, internal helpers in camelCase, and file names in lowercase with underscores only when they improve clarity (for example `inline_data_url_cache.go`). Keep package scope in `package main` unless a real reusable package boundary emerges. Add short Chinese comments only for non-obvious proxy, cache, or timeout logic.

## Testing Guidelines
Use the standard library `testing` package; HTTP behavior is validated with `net/http/httptest`. Name tests as `TestXxx`, keep them in the matching `*_test.go` file, and prefer table-driven tests for env parsing, domain matching, and proxy edge cases. Any change to routing, cache TTL behavior, image fetch rewriting, or admin auth should include or update tests. Keep local test runs under 60 seconds.

## Commit & Pull Request Guidelines
Recent history follows a lightweight Conventional Commit style such as `feat: ...` and `Refactor: ...`. Prefer lowercase prefixes like `feat:`, `fix:`, `refactor:`, and keep subjects imperative and specific. PRs should include: a short summary, affected env vars or routes, test commands run, and sample requests or admin UI screenshots when behavior changes.

## Security & Configuration Tips
Never commit real API keys or production URLs. Document new environment variables in `README.md` and this guide when contributor workflow changes. For proxy-related changes, call out default values, fail-open/fail-closed behavior, and any TLS or allowlist impact.
