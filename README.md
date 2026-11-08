# Cloudflare DDNS Go

A small daemon that keeps a Cloudflare DNS `A` record pointed at your host's public IPv4 address.

Every 5 minutes (configurable) it looks up the current public IP from Cloudflare's [`1.1.1.1/cdn-cgi/trace`](https://1.1.1.1/cdn-cgi/trace), falling back to [icanhazip.com](https://icanhazip.com/) if that fails. It updates the record only when the address has changed. It ships as a static binary in a `scratch` container image.

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
| `CLOUDFLARE_RECORD_NAME` | Yes | Full record name, e.g. `home.example.com` |
| `SYNC_INTERVAL` | No | How often to check the public IP, as a Go duration such as `10m`. Between `30s` and `24h` (default `5m`) |
| `CLOUDFLARE_RECORD_TTL` | No | Record TTL in seconds: `1` for automatic, or `30` to `86400`. Cloudflare only accepts values below `60` on Enterprise plans (default `300`) |
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

## Behaviour

- Records are always written as DNS-only (not proxied), with the configured TTL.
- Only IPv4 is supported. The IP lookup is forced over IPv4.
- If the Cloudflare lookup fails, the failure is logged and icanhazip.com is used for that sync. Cloudflare also runs icanhazip.com, so the fallback covers one endpoint failing, not a Cloudflare-wide outage.
- Temporary failures, such as network errors or Cloudflare `5xx` responses, are logged and retried on the next cycle.
- Permanent failures make the daemon exit with a non-zero status, so the problem shows up instead of being retried silently. These are a rejected token (`401`/`403`), a missing record (`404`), or a record that is not of type `A`. Compose's `restart: always` then restarts the container.

## Health check

The daemon serves `GET /healthz` on `HEALTH_ADDR`. It returns `200` while the last successful sync is less than two and a half sync intervals plus 90 seconds old (14 minutes by default), and `503` otherwise. The extra 90 seconds covers one slow sync: two IP lookups and a record update.

The Compose healthcheck runs `/cloudflare-ddns healthcheck`, which probes that endpoint from inside the container.

## Development

Requires Go 1.27 or newer.

```sh
go test -race ./...
golangci-lint run
go build -o cloudflare-ddns
```

CI runs formatting, `go vet`, the tests, golangci-lint, govulncheck and a Docker build on every push to `master` and on pull requests.

## License

Distributed under [AGPL-3.0-only](LICENSE).
