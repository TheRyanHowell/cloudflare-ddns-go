# Cloudflare DDNS Go

A small daemon that keeps a Cloudflare DNS `A` record pointed at your host's public IPv4 address.

Every 5 minutes (configurable) it looks up the current public IP, trying a list of providers in order until one answers. It updates the record only when the address has changed. It ships as a static binary in a `scratch` container image.

## Requirements

- Docker with Docker Compose
- A Cloudflare API token with **Zone → DNS → Edit** permission for the zone
- An existing `A` record to manage (the daemon updates records but does not create them)

## Configuration

Copy the example environment file and fill it in:

```sh
cp .env.example .env
```

| Variable | Required | Description |
|---|---|---|
| `CLOUDFLARE_API_KEY` | Yes | Cloudflare API token (sent as a Bearer token) |
| `CLOUDFLARE_ZONE_ID` | Yes | Zone ID, shown on the zone's Overview page in the Cloudflare dashboard |
| `CLOUDFLARE_RECORD_ID` | Yes | ID of the `A` record to update |
| `CLOUDFLARE_RECORD_NAME` | Yes | The record's full name, e.g. `home.example.com`. Unicode names are accepted |
| `SYNC_INTERVAL` | No | How often to check the public IP, as a Go duration such as `10m`. Between `30s` and `24h` (default `5m`) |
| `CLOUDFLARE_RECORD_TTL` | No | Record TTL in seconds: `1` for automatic, or `30` to `86400`. Cloudflare only accepts values below `60` on Enterprise plans (default `300`) |
| `IP_PROVIDERS` | No | Comma-separated public IP providers, in the order to try them (default: all of them, see [IP providers](docs/ip-providers.md)) |
| `HEALTH_ADDR` | No | Listen address for the health endpoint (default `127.0.0.1:8080`) |

To find the record ID, list the zone's records:

```sh
curl -s -H "Authorization: Bearer $CLOUDFLARE_API_KEY" \
  "https://api.cloudflare.com/client/v4/zones/$CLOUDFLARE_ZONE_ID/dns_records?type=A"
```

## Usage

```sh
docker compose up -d
docker compose logs -f
```

### ARM

On an ARM machine, such as a Raspberry Pi, the commands above build a native image. To pin the platform, for example when building on another machine, add the matching override:

| File | Platform | Examples |
|---|---|---|
| `docker-compose.arm64.yml` | `linux/arm64` | Raspberry Pi 3, 4 or 5 on a 64-bit OS |
| `docker-compose.armv7.yml` | `linux/arm/v7` | Raspberry Pi 2, 3 or 4 on a 32-bit OS |
| `docker-compose.armv6.yml` | `linux/arm/v6` | Raspberry Pi Zero or 1 |

```sh
docker compose -f docker-compose.yml -f docker-compose.arm64.yml up -d
```

The Dockerfile cross-compiles, so building an ARM image on an x86 machine needs no emulation.

## How it works

- Records are always written as DNS-only (not proxied), with the configured TTL.
- Only IPv4 is supported.
- The container runs with least privilege: a non-root user (UID 65532), a read-only filesystem, no Linux capabilities and `no-new-privileges`.
- The public IP comes from the first provider in `IP_PROVIDERS` that answers. Providers are reached over HTTPS, and the default list spans eight independent networks: `cloudflare`, `amazon`, `akamai`, `opendns`, `ripe`, `hetzner`, `mullvad`, `ipinfo`, `ipify` and `icanhazip`. See [IP providers](docs/ip-providers.md).
- A provider that fails is skipped for a while, and a local network outage costs only a few requests per sync. See [failure handling](docs/failure-handling.md).
- Temporary Cloudflare errors are retried at the next sync. A rejected token or a wrong record ID makes the daemon report unhealthy and exit after an hour, so it retries hourly.

## Health check

The daemon serves `GET /healthz` on `HEALTH_ADDR`. It returns `200` while syncs are succeeding and `503` once the last success is too old (18 minutes with the defaults) or after a permanent failure. The [failure handling](docs/failure-handling.md#health-window) page explains the window.

The Compose healthcheck runs `/cloudflare-ddns healthcheck`, which probes that endpoint from inside the container.

## Documentation

- [IP providers](docs/ip-providers.md): the providers, their networks, rate limits and default order
- [Failure handling](docs/failure-handling.md): cooldowns, outages, permanent failures and the health window
- [Troubleshooting](docs/troubleshooting.md): what the log messages mean and how to fix them
- [Architecture](docs/architecture.md): how the code is organised and how a sync works

## Development

Requires Go 1.27 or newer.

```sh
go test -race ./...
golangci-lint run
go build -o cloudflare-ddns
```

CI runs the tests, golangci-lint (including `gofmt` and `govet`), govulncheck and a Docker build for amd64, arm64, armv7 and armv6 on every push to `master` and on pull requests.

## License

Distributed under [AGPL-3.0-only](LICENSE).
