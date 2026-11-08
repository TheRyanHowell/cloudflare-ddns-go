# Failure handling

How the daemon reacts when IP providers or the Cloudflare API fail.

## Failing IP providers

- If a provider fails, the failure is logged and the next provider in `IP_PROVIDERS` is tried.
- A provider that fails is skipped for the next sync, then the next 2, 4… syncs with each consecutive failure, up to about an hour (or one interval, if longer). One success resets it.
- A `Retry-After` header (for example on `429 Too Many Requests`) is honoured, up to 24 hours.
- If `IP_PROVIDERS` names a single provider, it is never skipped, since there is nothing to fall back to. It is asked at every sync even while it keeps failing, unless it sends `Retry-After`.
- If every provider is cooling down, the sync makes no requests and is retried at the next interval.

## Local network outages

A provider that cannot be reached at all (a DNS, connection, TLS or timeout error) usually means the host's own connection is down, so these failures are treated gently:

- After 3 providers in a row cannot be reached, a sync stops rather than trying the rest, as long as providers that are not cooling down remain. The next sync starts with the providers not yet asked, so an outage costs at most 3 requests per sync. Those 3 are put on a normal cooldown once another provider answers.
- If every provider fails in the same sync, those that could not be reached start no cooldown and are tried again at the next sync. A provider that answered with an error, such as `403` or `429`, still goes on cooldown.

## Cloudflare API failures

- Temporary failures, such as network errors or Cloudflare `5xx` responses, are logged and retried at the next sync.
- Permanent failures are a rejected token (`401`/`403`), a missing record (`404`), a record that is not of type `A`, or a record whose name is not `CLOUDFLARE_RECORD_NAME`. The name check stops a mistyped record ID from renaming another record.
- After a permanent failure, the daemon logs the error, `/healthz` returns `503`, and the daemon exits with a non-zero status after an hour. Compose's `restart: always` then restarts it, so it retries hourly rather than in a tight loop. Stopping the container during that hour exits at once.

## Health window

`/healthz` returns `200` while the last successful sync is younger than two and a half sync intervals plus an allowance for one slow sync, and `503` otherwise.

The allowance is 30 seconds per IP provider plus 30 seconds for the record update. With the defaults (a 5-minute interval and 10 providers) the window is 12.5 + 5.5 = 18 minutes.
