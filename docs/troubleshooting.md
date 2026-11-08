# Troubleshooting

Log lines below are in the daemon's format, for example `level=ERROR msg=exiting error="..."`. Read them with `docker compose logs -f`.

After changing `.env`, run `docker compose up -d` so the container is recreated with the new values. A plain restart keeps the old ones.

## The container exits at once and keeps restarting

The configuration was rejected. The log line is `msg=exiting` with one of these errors:

| Error | Fix |
|---|---|
| `API token is not set`, `zone ID is not set`, … | Set the missing setting, as a flag or in `.env` (see the README's configuration table) |
| `set the API token only once, either directly or as a file` | The token is given both directly and as a file. Keep one |
| `read API token file: ...` or `API token file "..." is empty` | The token file is missing, unreadable by UID 65532, or empty |
| `record name "home" must be the record's full name` | Use the full name, such as `home.example.com`, not `home` or `@` |
| `IP provider "..." must be one of ...` | Use names from [IP providers](ip-providers.md), separated by commas |
| `IP providers "..." have an empty entry` or `name "..." more than once` | Remove the stray comma or the duplicate |
| `sync interval "..." must be a duration between 30s and 24h0m0s` | Use a Go duration such as `10m` |
| `record TTL "..." must be 1 (automatic) or between 30 and 86400 seconds` | Use `1` or a number of seconds in that range |
| `health server: listen tcp ...` | The health address is not a valid `host:port` or the port is in use. Use something like `127.0.0.1:8080` |

## `permanent failure, exiting after a delay`

The daemon reached Cloudflare but cannot manage the record. It reports unhealthy and exits after an hour, then Compose restarts it. Fix the cause and run `docker compose up -d`.

| Error contains | Cause and fix |
|---|---|
| `unexpected status 401` or `403` | The token is wrong, expired or lacks permission. It needs **Zone → DNS → Edit** on this zone. For a user-owned token, check it with `curl -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" https://api.cloudflare.com/client/v4/user/tokens/verify` |
| `unexpected status 404` | The zone ID or record ID is wrong. List the zone's records with the `curl` command in the README and copy the `id` of the record you want |
| `invalid DNS record: type "AAAA", want "A"` | The record ID points to a record that is not an `A` record |
| `invalid DNS record: name "...", want "..."` | The record ID belongs to a different record than the configured record name. Check both; the daemon refuses rather than renaming the wrong record |
| `invalid DNS record: no content` | The record has no address. Give it any IPv4 address in the dashboard and the daemon will correct it |

## `sync failed, will retry`

A single sync failed and is retried at the next interval. Occasional lines are normal.

- If it repeats with `unexpected status 400` and mentions the TTL, the record TTL is below `60`, which only Enterprise plans accept. Use `1` (automatic) or `60` and above.
- If it repeats with `every public IP provider is cooling down after failures; next retry in ...`, no provider has answered for a while. See below.

## No provider answers

Lines such as `public IP source failed` for every provider, followed by `every public IP provider is cooling down`, mean the container cannot reach any provider.

- Check outbound HTTPS and DNS from the host: `curl -4 https://checkip.amazonaws.com/`. The image has no shell, so test from the host or another container on the same network.
- The daemon needs IPv4. An IPv6-only network cannot work.
- `public IP from https://...: "..." is not an IPv4 address` usually means a captive portal, a proxy or a filtering DNS server answered instead of the provider.

During a real outage the daemon sends at most three requests per sync and recovers on its own when the network returns.

## One provider keeps failing

`public IP source failed` for one provider, or `public IP source was unreachable while others answered`, means that provider is down, rate-limiting, or blocked by a firewall or DNS filter (for example a Pi-hole blocklist). The daemon skips it for longer each time and uses the others, so nothing needs doing. To silence it, leave it out of `IP_PROVIDERS`.

`ipinfo` allows 1,000 requests a day per IP address without an account. Other software on the same connection can use that up.

## The container is unhealthy

`docker inspect --format '{{json .State.Health}}' <container>` shows only `unhealthy: 503 Service Unavailable`. The reason is in the daemon's logs:

| Log line | Meaning |
|---|---|
| `loading record failed, will retry` | The daemon has not loaded the record since it started, usually a network or Cloudflare problem |
| `sync failed, will retry`, repeated | Syncs have failed for longer than the [health window](failure-handling.md#health-window); the error says why |
| `permanent failure, exiting after a delay` | See [permanent failure](#permanent-failure-exiting-after-a-delay) above |

## The record is wrong but the daemon does not fix it

The daemon reads the record once at startup and afterwards only updates it when the public IP changes. If the record is edited or deleted in the Cloudflare dashboard while the IP stays the same, restart the container to correct it.
