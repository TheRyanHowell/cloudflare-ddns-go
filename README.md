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
| `CLOUDFLARE_RECORD_NAME` | Yes | Full record name, e.g. `home.example.com` |
| `SYNC_INTERVAL` | No | How often to check the public IP, as a Go duration such as `10m`. Between `30s` and `24h` (default `5m`) |
| `CLOUDFLARE_RECORD_TTL` | No | Record TTL in seconds: `1` for automatic, or `30` to `86400`. Cloudflare only accepts values below `60` on Enterprise plans (default `300`) |
| `IP_PROVIDERS` | No | Comma-separated public IP providers, in the order to try them (default: every provider, in the order of the [IP providers](#ip-providers) table) |
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
- If an IP provider fails, the failure is logged and the next provider in `IP_PROVIDERS` is tried.
- A provider that fails is skipped for a cooldown: 1 minute after one failure, doubling with each consecutive failure up to 1 hour. One success resets it. A single failure therefore does not drop a provider from the next sync at the default interval. A `Retry-After` header (for example on `429 Too Many Requests`) is honoured, up to 24 hours.
- If every provider fails in the same sync, those that could not be reached at all (DNS, connection, TLS or timeout errors) start no cooldown, since the local network is the likely cause, and are tried again at the next sync. A provider that answered with an error, such as `403` or `429`, still goes on cooldown.
- If every provider is cooling down, the sync makes no requests and is retried at the next interval.
- Temporary failures, such as network errors or Cloudflare `5xx` responses, are logged and retried on the next cycle.
- Permanent failures make the daemon exit with a non-zero status, so the problem shows up instead of being retried silently. These are a rejected token (`401`/`403`), a missing record (`404`), or a record that is not of type `A`. Compose's `restart: always` then restarts the container.

## IP providers

| Name | Endpoint | Network | Notes |
|---|---|---|---|
| `cloudflare` | `https://api.cloudflare.com/cdn-cgi/trace` | Cloudflare (AS13335) | |
| `amazon` | `https://checkip.amazonaws.com/` | AWS (AS16509) | |
| `akamai` | `https://whatismyip.akamai.com/` | Akamai (AS20940) | |
| `opendns` | `https://doh.opendns.com/dns-query` | Cisco OpenDNS (AS36692) | Asks for `myip.opendns.com` over DNS-over-HTTPS |
| `ipinfo` | `https://ipinfo.io/ip` | Google Cloud (AS396982) | Limited to 1,000 requests a day per IP without an account |
| `ipify` | `https://api4.ipify.org/` | Cloudflare (AS13335) | |
| `icanhazip` | `https://icanhazip.com/` | Cloudflare (AS13335) | Owned and run by Cloudflare |

By default every provider is tried, in the order of this table. The first five are on five independent networks, so a single provider or network outage does not stop updates. The rate-limited `ipinfo` comes after the unlimited independent providers, and the two extra Cloudflare-backed providers come last. Set `IP_PROVIDERS` to use a subset or a different order. Networks are the autonomous system (ASN) announcing the address each endpoint resolved to over IPv4, checked against RIPEstat in November 2026. They may change.

Only ipinfo documents a rate limit. ipify states it has none, and the others publish nothing. The cooldowns keep the daemon well within the limits either way: at the default 5-minute interval it makes at most 288 lookups a day, and later providers are only asked when earlier ones fail.

Every provider is reached over HTTPS, so the answer is protected in transit. `opendns` uses DNS-over-HTTPS rather than plain DNS because `opendns.com` is not DNSSEC-signed, and OpenDNS generates the `myip.opendns.com` answer itself, so it could never be signed. For the same reason, Google's `o-o.myaddr.l.google.com` is not supported: it only works over plain, unauthenticated DNS.

## Health check

The daemon serves `GET /healthz` on `HEALTH_ADDR`. It returns `200` while the last successful sync is less than two and a half sync intervals old plus an allowance for one slow sync, and `503` otherwise. The allowance is 30 seconds per IP provider plus 30 seconds for the record update, so the window is 16.5 minutes with the defaults.

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
