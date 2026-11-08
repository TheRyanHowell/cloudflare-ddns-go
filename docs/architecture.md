# Architecture

The module `github.com/TheRyanHowell/cloudflare-ddns-go` follows Go's [module layout](https://go.dev/doc/modules/layout): the command lives in `cmd/`, packages only the command needs live in `internal/`, and there is no framework.

| Package | Responsibility |
|---|---|
| `cmd/cloudflare-ddns` | The binary. `main.go` turns SIGINT and SIGTERM into a cancelled context, `app.go` runs either the daemon or the `healthcheck` subcommand and wires the packages together, and `config.go` reads and validates the environment variables |
| `ddns` | The sync loop. `Syncer.Run` loads the record, then compares and updates it every interval; `Syncer.Sync` is a single read, compare and update |
| `publicip` | Finds the public IPv4 address: the provider registry, response parsers and the fallback with cooldowns (`New`, `Lookup.Current`) |
| `cloudflare` | Reads and updates the DNS record through the Cloudflare API (`NewRecord`, `Record.Content`, `Record.Update`) and decides which of its errors are permanent (`IsPermanent`) |
| `internal/fetch` | Shared HTTP request helper and error classification |
| `internal/health` | The `/healthz` endpoint and the probe the `healthcheck` subcommand runs |

`ddns` depends only on small interfaces it declares itself: `RecordStore` (`Content`, `Update`), `IPSource` (`Current`) and an optional `HealthReporter`. The command passes it a `cloudflare.Record`, a `publicip.Lookup`, the `health.State` and `cloudflare.IsPermanent` as the permanent-error check, so `ddns` imports neither `cloudflare` nor the health code.

## Startup

In `cmd/cloudflare-ddns`, `run` loads the configuration, then starts two goroutines under one `errgroup`: the health server and the syncer. If either stops with an error, the other is cancelled and the process exits.

```mermaid
flowchart LR
    main --> run
    run --> config[loadConfig]
    run --> group{errgroup}
    group --> health[health server]
    group --> syncer
    syncer --> record[cloudflare.Record]
    syncer --> lookup[publicip.Lookup]
    lookup --> providers[publicIPLookup per provider]
```

## A sync

1. At startup the syncer reads the record's current address with `cloudflare.Record.Content`. That call also checks the record is an `A` record with the configured name. Temporary errors are retried every 30 seconds.
2. Every interval it asks `publicip.Lookup.Current` for the public IP.
3. If the address differs from the last known one, it calls `cloudflare.Record.Update`, which sends a `PUT` with the new address, the name, the TTL and `proxied: false`.
4. Each successful sync marks the health state, which keeps `/healthz` at `200`.

A permanent error, such as a rejected token, records a permanent failure in the health state and ends the syncer after an hour (see [failure handling](failure-handling.md)).

## Finding the public IP

`publicip` has three layers:

- **The provider registry** (`ipProviders`) is an ordered list, built afresh on each call so no state is shared. Each entry has a URL, an optional DNS fallback URL, a function that extracts the address from the response (plain text, Cloudflare's `key=value` trace, JSON or a DNS-over-HTTPS answer) and an optional `Accept` header. Its order is the default `IP_PROVIDERS` order, which `publicip.Providers` returns; `publicip.New` rejects unknown or repeated names.
- **`publicIPLookup`** asks one provider. It fetches the URL, extracts the address, and accepts it only if it parses as IPv4.
- **`Lookup`** tries the providers in order and remembers failures per provider. It applies the cooldowns, the early stop during outages and `Retry-After`.

All lookups share one HTTP client that only dials over IPv4, so a dual-stack host never reports an IPv6 address.

## Errors

`fetch.Body` sorts failures into kinds, which the rest of the code acts on:

- **`fetch.TransportError`**: no usable response arrived (DNS, connection, TLS, timeout, or a body cut short). `publicip` uses it to tell a local outage from a provider that answered with an error.
- **`fetch.StatusError`**: a non-`200` response, carrying the status code and any `Retry-After` (read with `fetch.RetryAfter`).
- **Permanent errors** (`cloudflare.IsPermanent`): `401`, `403`, `404` and `cloudflare.ErrInvalidRecord`. The syncer stops on these instead of retrying.

## Testing

Tests sit beside the code in each package, and fuzz tests cover the parsers and validators. They run against `httptest` servers instead of the real providers or Cloudflare, and the fallback tests use an injected clock, so the suite runs in about a second. Run it with `go test -race ./...`.
