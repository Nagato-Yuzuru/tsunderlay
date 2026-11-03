# Contributing

Tools come from `mise.toml`: run `mise install` once. `mise run lint` runs
golangci-lint, and `mise run test` runs the unit tests. `mise run vm:e2e` runs
the end-to-end tests in a new tart guest. The end-to-end tests change routes
and interfaces, so `mise run e2e` runs them on this host only when the
root-owned file `/var/db/tsunderlay-e2e-disposable` marks the host as
disposable. No task makes this file on your host. `prek run --all-files` runs
the same checks as CI.
