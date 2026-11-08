# Cloudflare DDNS Go

A small daemon that keeps a Cloudflare DNS `A` record pointed at your host's public IPv4 address.

Every 5 minutes it looks up the current public IP from [icanhazip.com](https://icanhazip.com/), and it updates the record only when the address has changed. It ships as a static binary in a `scratch` container image.

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

- Records are always written as DNS-only (not proxied), with a TTL of 300 seconds.
- Only IPv4 is supported. The IP lookup is forced over IPv4.
- Temporary failures, such as network errors or Cloudflare `5xx` responses, are logged and retried on the next cycle.
- Permanent failures make the daemon exit with a non-zero status, so the problem shows up instead of being retried silently. These are a rejected token (`401`/`403`), a missing record (`404`), or a record that is not of type `A`. Compose's `restart: always` then restarts the container.

## Health check

The daemon serves `GET /healthz` on `HEALTH_ADDR`. It returns `200` while the last successful sync is less than 12.5 minutes old, and `503` otherwise.

The Compose healthcheck runs `/cloudflare-ddns healthcheck`, which probes that endpoint from inside the container.

## Development

Requires Go 1.27 or newer.

```sh
go test ./...
go build -o cloudflare-ddns
```

## License

Distributed under [AGPL-3.0-only](LICENSE).
