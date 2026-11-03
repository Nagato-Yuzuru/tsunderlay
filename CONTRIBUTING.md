# Contributing

## What to run, and when

| When | Run | Does CI run it? |
| --- | --- | --- |
| Once, after you clone | `mise install`. It installs the pinned tools and the pre-commit hook. | — |
| Each commit | Nothing: the pre-commit hook runs. If the hook changes files, add them and commit again. | Yes, on all files. |
| Before you open a pull request | `go test -race ./...` | Yes. |
| You changed a log attribute or `telemetry/` | `mise run telemetry:live-check`. The hook regenerates `attrs.go` and `docs/telemetry.md`. | Yes. |
| You changed how routes are read or written, `install`, `uninstall`, or the daemon | `mise run vm:e2e`. It needs a Mac with Apple silicon. | Only in part: CI does not run the tests that need a guest (install, the fake VPN, the daemon). |

`mise tasks` lists all tasks and what each one does.

The end-to-end tests add routes and interfaces as root. `mise run vm:e2e`
runs them in a new tart guest and deletes the guest on a pass. Do not create
`/var/db/tsunderlay-e2e-disposable` on your own Mac: with it, `mise run e2e`
changes the routes of the host.

## Pull requests

We squash-merge each pull request, and we can edit its title. The commits in
a pull request need no special format.
