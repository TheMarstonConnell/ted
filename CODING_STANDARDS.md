Tautological tests considered harmful.

Keep comments short. Delete any claim from comments that is derivable from the code.

For Ted's web UI, follow [the design system](web/DESIGN_SYSTEM.md) and
[interface conventions](web/README.md). Runtime and API contracts live in
[the control-plane docs](docs/control-plane.md), [workspace docs](docs/workspaces.md),
and [API docs](api/README.md).

Use the Go version in `go.mod` and format Go changes with `gofmt`. Keep generated
API code in sync with its schema; do not edit generated files as the source of
truth. Existing validation commands are in `.github/workflows/test.yml`:

- Build embedded web assets before Go checks: `cd web && npm ci && npm run build`.
- From the root: `./api/check-generated.sh`, `go vet ./...`, and `go test -race ./...`.
- From `web/`: `npm run generate:api`, `npm run lint`, `npm test`,
  `npm run test:e2e` (requires Playwright Chromium), and `npm run build`.
- For review automation: `node --test .github/codex/*.test.cjs`.

Automated reviews should not repeat findings already enforced by these tools.
