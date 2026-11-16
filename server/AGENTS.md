# Server agent guide

This adds what differs inside the `server` module; the root
[`AGENTS.md`](../AGENTS.md) still applies. Read the `go-server` skill before
changing server code, and [`cluster/GUARANTEES.md`](cluster/GUARANTEES.md)
before changing cluster delivery or recovery.

## Local rules

- **The module root:** it builds and runs one node. A host command that needs
  the application goes through `server.New`. One that only opens the
  configuration or the database lives in `operator`, which never imports the
  application, a transport or the module root.
- **Tests:** plain `go test ./...` is network-free. External services use the
  `integration` build tag and the dedicated Make targets. Wiring tests use the
  real `server.New` graph through `testlib`; don't build a second composition
  path.
- **Ports:** use PostgreSQL on 25432 and Redis on 26379 locally, for example
  `make -C server integration-postgres PROCTOR_POSTGRES_PORT=25432`.

## Checks

- `make -C server check`: the hermetic full gate.
- `make -C server architecture`: the import-direction policy and license
  headers.
- `make -C server openapi-check`, `errors-check` and `i18n-check`: the
  contract agreements.
- `../.build/bin/golangci-lint run --config ../.golangci.yml <packages>`: lint
  for the packages you touched. Run `make tools` once first.

The integration targets are described in the
[testing guide](../docs/public/developers/testing.mdx).
