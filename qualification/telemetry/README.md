# Service telemetry qualification

Run from the repository root with a verified official collector binary:

```sh
python3 qualification/telemetry/run.py --collector /absolute/path/to/otelcol-contrib
```

The runner starts the collector with OTLP/gRPC, a file exporter and its health
extension, then invokes the actual Accounts and auth-gateway metrics initializers
in tagged Go tests. It checks the collector output for each service's configured
identity, environment, runtime metrics and a counter increment. Multiple records
of value one are cumulative snapshots from flush and shutdown, not multiple
metric export paths. Qualification passed with `otelcol-contrib` 0.158.0.

This exercises service metric initialization and real OTLP transport locally.
By default it does not boot either full service. It does not apply Kubernetes
network policy, reconcile External Secrets, or verify a deployed backend. The runner's supplied endpoint
is an integration fixture; deployed endpoint resolution is a separate gate.

For development across the pending Core and SDK changes, add `--core /path/to/core`
and `--sdk /path/to/sdk-go`. The runner makes temporary modfiles, leaves release
pins unchanged, and reports `development_overrides: true`. This mode proves the
local source combination only. Qualification without overrides is required after
the dependency releases have been adopted.

## Full Accounts runtime

Add `--full-accounts` to exercise Accounts' actual `main()` in a child test
process, with real dependencies managed by Codefly and the `dev-admin` fixture.
The test checks `/v1/status`, calls the public `Version` RPC, and shuts down
gracefully. The runner requires the collector to receive both the request span
and Go runtime metrics with the Accounts identity and a distinct full-service
resource attribute. A metrics-initializer pass cannot satisfy this check.

```sh
GOWORK=off python3 qualification/telemetry/run.py \
  --collector /absolute/path/to/otelcol-contrib \
  --core /absolute/path/to/core --sdk /absolute/path/to/sdk-go \
  --full-accounts
```

The required Codefly CLI and service agents must be available on `PATH` or
through the workspace's agent selections. This runs production application
startup and handlers inside a test executable; it does not qualify a released
application binary. Use `--full-gateway` for the corresponding gateway entrypoint check.

## Full gateway runtime

`--full-gateway` starts the gateway's real `main()` in a child test process.
Codefly starts its real Accounts/cache dependencies. The test waits for `/ready`
to return HTTP 200 and `status: ok`, then shuts down gracefully. The collector
must receive `go.goroutine.count` and the HTTP request span `GET /ready` under
the gateway identity and a full-service resource marker. That marker is applied
after dependencies start so their exports cannot satisfy the gateway check.
This proves readiness-route instrumentation, not an authenticated business flow.

After released dependencies are adopted, both full checks run with:

```sh
python3 qualification/telemetry/run.py \
  --collector /absolute/path/to/otelcol-contrib \
  --full-accounts --full-gateway
```

For unreleased dependencies, the gateway's Accounts dependency also needs local
build source selection. The runner's `--core`/`--sdk` modfiles affect its Go test
commands only. Codefly supports `codefly agent deps --link --dir .` from this
repository in a development workspace containing local `core` and `sdk-go`.
Temporarily select `spec.with-workspace: true` in the Accounts and gateway
service manifests and run with `GOWORK` pointing to that linked `go.work`.
Restore those manifest settings afterward; do not publish local workspace
selection as part of the module. The result reports the selected `go_workspace`
and marks such a run as using development overrides.

Per-service `--set` flags are runtime overrides, applied after compilation;
`--set GOFLAGS=...` cannot select dependencies for the build.

## Reference-branch status (2026-10-06)

This is comparison material, not a release-ready change. Consider the approach
and tests alongside alternative implementations. The branch includes the
prerequisite patch from pull request #1025 at
`eaef3594bcae4f2ac18722cded9d63ecf4f5ec77`.

The full local application checks passed with development dependencies. The
full development CI run had 37 passed tasks, 1 failed and 11 skipped: Accounts
container building failed because workspace image contexts are deliberately
unsupported. Published dependency pins do not yet contain the required shared
telemetry API. Neither result establishes release readiness.

The workspace retains local Go-gRPC/Vault agent selections; their version labels
do not establish that published assets contain the tested source changes. Review
and release the shared dependencies, adopt their published pins through the
owning tooling, remove local selections and rerun with workspace mode off before
shipping. The managed secrets agent also has an unresolved durable-provisioning
test failure. No deployed-backend qualification was performed.
