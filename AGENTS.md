# AGENTS.md

- Before you finish, `gofmt -l .` must print nothing and `go vet ./...`,
  `go test -race ./...` must pass, in the root and in `standalone/`. Give
  every test run a `-timeout`.
- Standard library only, also for tests; `deps_test.go` checks it.
- `pathcost`, `route` and `probe` are pure: no clock, no IO, no
  goroutines. Time is a parameter.
- One cost function: every comparison of paths goes through
  `pathcost.Path`; do not add a second formula.
- Results are deterministic: ties break on node ids, never on map order.
- The README, docs and commits name no other project and quote no
  benchmark numbers.
- Commit subjects are short, English and imperative.
