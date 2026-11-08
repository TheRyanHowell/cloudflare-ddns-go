# Cloudflare DDNS Go

Keeps a Cloudflare DNS `A` record pointed at your public IPv4 address. It checks every 5 minutes, asking a list of IP providers in turn, and updates the record only when the address changes.

Run it as a Docker container, as a standalone binary, or use it as a Go library.

## Quick start

You need Docker with Docker Compose, a Cloudflare API token with **Zone → DNS → Edit** permission, and an existing `A` record to manage.

```sh
cp .env.example .env   # fill in the token, zone ID, record ID and record name
make run               # build and start the daemon
make logs              # follow its logs
```

`make run` builds a native image for this machine's architecture. To build for another, set `PLATFORM`, for example `make run PLATFORM=linux/arm/v7`. Without `make`, use `docker compose up -d`, with `DOCKER_DEFAULT_PLATFORM` to pick a platform. Run `make help` for every target.

Supported platforms: `linux/amd64`, `386`, `arm64`, `arm/v7`, `arm/v6`, `arm/v5`, `riscv64`, `ppc64le`, `s390x`, `mips64le` and `loong64`.

## Install the binary

Download a binary for Linux, macOS, Windows or FreeBSD from the [releases page](https://github.com/TheRyanHowell/cloudflare-ddns-go/releases), or install it with Go 1.27 or newer:

```sh
go install github.com/TheRyanHowell/cloudflare-ddns-go/cmd/cloudflare-ddns@latest
```

## Configuration

Each setting is an environment variable or a flag. A flag you set wins.

| Variable | Flag | Required | Description |
|---|---|---|---|
| `CLOUDFLARE_API_TOKEN` | `--token` | Yes, or the next | Cloudflare API token |
| `CLOUDFLARE_API_TOKEN_FILE` | `--token-file` | | Path to a file holding the token, such as a Docker secret |
| `CLOUDFLARE_ZONE_ID` | `--zone-id` | Yes | Zone ID |
| `CLOUDFLARE_RECORD_ID` | `--record-id` | Yes | ID of the `A` record to update |
| `CLOUDFLARE_RECORD_NAME` | `--record-name` | Yes | The record's full name, such as `home.example.com` |
| `SYNC_INTERVAL` | `--interval` | No | How often to check, from `30s` to `24h` (default `5m`) |
| `CLOUDFLARE_RECORD_TTL` | `--ttl` | No | Record TTL in seconds: `1` for automatic, or `30` to `86400` (default `300`). Values below `60` need an Enterprise plan |
| `IP_PROVIDERS` | `--providers` | No | Comma-separated [IP providers](docs/ip-providers.md) to try, in order (default: all) |
| `HEALTH_ADDR` | `--health-addr` | No | Health endpoint address (default `127.0.0.1:8080`) |

[Configuration](docs/configuration.md) covers creating the token, using a Docker secret, finding the record ID, and how flags and the environment combine.

## Commands

| Command | What it does |
|---|---|
| `run` | Runs the daemon. This is the default when no command is given |
| `sync` | Updates the record once if needed, prints what it did, and exits |
| `ip` | Prints the public IP address |
| `healthcheck` | Probes a running daemon's health endpoint |
| `version` | Prints the version |
| `completion` | Prints a shell completion script |

`cloudflare-ddns <command> --help` lists a command's flags. Exit codes are `0` for success, `1` for an error and `2` for a usage mistake.

To update the record from cron instead of running the daemon:

```cron
*/10 * * * * cloudflare-ddns sync --token-file /etc/cloudflare-ddns/token --zone-id ZONE_ID --record-id RECORD_ID --record-name home.example.com
```

## How it works

- The record is written as DNS-only (not proxied), with the configured TTL. Only IPv4 is supported.
- Every IP provider is reached over HTTPS. The default list spans eight independent networks, and a provider that fails is skipped for a while.
- Temporary errors are retried at the next sync. A rejected token or a wrong record ID makes the daemon report unhealthy and exit after an hour, so a restarting container retries hourly.
- `GET /healthz` returns `200` while syncs succeed and `503` when they stop (after 18 minutes with the defaults). The Compose healthcheck uses it.
- The container runs as a non-root user with a read-only filesystem and no Linux capabilities.

## Documentation

- [Configuration](docs/configuration.md): the token, Docker secrets, finding the record ID, and flags versus the environment
- [IP providers](docs/ip-providers.md): the providers, their networks, rate limits and default order
- [Failure handling](docs/failure-handling.md): cooldowns, outages, permanent failures and the health window
- [Troubleshooting](docs/troubleshooting.md): what the log messages mean and how to fix them
- [Library](docs/library.md): using the Go packages
- [Architecture](docs/architecture.md): how the code is organised and how a sync works

## Development

Requires Go 1.27 or newer.

```sh
make check    # tests with the race detector, golangci-lint and govulncheck
make build    # build ./cloudflare-ddns
make images   # build the image for every platform (needs a buildx builder)
```

Platforms are listed once, in `PLATFORMS` in the `Makefile`. CI runs the same checks and builds every platform on each push to `master` and on pull requests.

Releases follow [Semantic Versioning 2.0](https://semver.org/). `make release VERSION=v1.2.3` checks the version and creates a signed tag locally (set up [git tag signing](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-tags) first); pushing the tag (`git push origin v1.2.3`) builds and publishes the release with binaries and checksums.

## License

Distributed under [AGPL-3.0-only](LICENSE).
