Cloudflare DDNS Go
==================

Updates Cloudflare DNS records with the current public IP address.

# Usage
Copy .env.example to .env and fill in the required values.

```sh
cp .env.example .env
```

Start the container

```sh
docker compose up -d
```

# Health check

The daemon serves `GET /healthz` on `HEALTH_ADDR` (default `127.0.0.1:8080`, loopback only). It returns 200 while the last successful sync is under 12.5 minutes old, and 503 otherwise. The compose healthcheck runs `cloudflare-ddns healthcheck`, which probes that endpoint.

Records are always set to DNS-only (not proxied). A rejected API token or a missing record makes the daemon exit, so `restart: always` shows the failure instead of retrying silently.

# License

Distributed under [AGPL-3.0-only](LICENSE).
