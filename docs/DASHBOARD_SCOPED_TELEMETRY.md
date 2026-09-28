# Dashboard-scoped telemetry

The dashboard now runs telemetry collection in its own process. It does not
install, start, or upgrade a system service. Closing the owning dashboard stops
collection, including if the process crashes or is killed. Multiple dashboards
share the socket; a remaining dashboard reconnects and takes over collection
after the owner exits (there may be a brief refresh delay).

Existing persistent services must be disabled separately. On Linux:

```sh
systemctl --user disable --now openusage-telemetry.service
```

Explicit `telemetry daemon run` and `telemetry daemon install` commands remain
available for users who deliberately want background collection. An already
running compatible collector is reused, not stopped by the dashboard.

While the dashboard is closed, hooks can ingest locally or spool events;
live API polling and maintenance stop. Provider history is caught up on the
next launch where the provider supports it. Short-lived quota observations
while closed may not be recoverable.

## Validation

Run sequentially under one four-core scope:

```sh
systemd-run --user --scope --collect -p CPUQuota=400% \
  env GOMAXPROCS=4 CGO_ENABLED=1 bash -c \
  'go test -p=4 -parallel=4 -race ./internal/daemon ./cmd/openusage -count=1 && go vet -p=4 ./internal/daemon ./cmd/openusage && make build GOFLAGS=-p=4'
```

Lifecycle tests use temporary sockets without polling real provider accounts.
