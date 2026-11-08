# IP providers

The daemon asks these services for the host's public IPv4 address. `IP_PROVIDERS` chooses which to use and in what order. By default every provider is tried, in the order of this table.

| Name | Endpoint | Network | Notes |
|---|---|---|---|
| `cloudflare` | `https://api.cloudflare.com/cdn-cgi/trace` | Cloudflare (AS13335) | Falls back to `https://1.1.1.1/cdn-cgi/trace` if the hostname cannot be resolved |
| `amazon` | `https://checkip.amazonaws.com/` | AWS (AS16509) | |
| `akamai` | `https://whatismyip.akamai.com/` | Akamai (AS20940) | |
| `opendns` | `https://doh.opendns.com/dns-query` | Cisco OpenDNS (AS36692) | Asks for `myip.opendns.com` over DNS-over-HTTPS |
| `ripe` | `https://stat.ripe.net/data/whats-my-ip/data.json` | RIPE NCC (AS3333) | Asks clients to register above 1,000 requests a day |
| `hetzner` | `https://ip.hetzner.com/` | Hetzner (AS24940) | |
| `mullvad` | `https://am.i.mullvad.net/json` | Mullvad (AS39351) | |
| `ipinfo` | `https://ipinfo.io/ip` | Google Cloud (AS396982) | Limited to 1,000 requests a day per IP without an account |
| `ipify` | `https://api4.ipify.org/` | Cloudflare (AS13335) | |
| `icanhazip` | `https://icanhazip.com/` | Cloudflare (AS13335) | Owned and run by Cloudflare |

## Default order

The first eight providers are on eight independent networks, so a single provider or network outage does not stop updates. The rate-limited `ipinfo` comes after them, and the two extra Cloudflare-backed providers come last.

Networks are the autonomous system (ASN) announcing the address each endpoint resolved to over IPv4, checked against RIPEstat in November 2026. They may change.

## Rate limits

Only ipinfo documents a hard rate limit, and RIPEstat asks heavy users to register. ipify states it has no limit, and the others publish nothing.

The daemon stays well within these limits. At the default 5-minute interval it makes at most 288 lookups a day, later providers are only asked when earlier ones fail, and failing providers are skipped for a while (see [failure handling](failure-handling.md)).

## Transport security

Every provider is reached over HTTPS, so the answer is protected in transit.

`opendns` uses DNS-over-HTTPS rather than plain DNS because `opendns.com` is not DNSSEC-signed, and OpenDNS generates the `myip.opendns.com` answer itself, so it could never be signed. For the same reason, Google's `o-o.myaddr.l.google.com` is not supported: it only works over plain, unauthenticated DNS.

## Cloudflare's endpoint

The `cloudflare` provider uses the `api.cloudflare.com` hostname because the bare `1.1.1.1` address intermittently returns error 1034. The IP address is used only when the hostname cannot be resolved, so a DNS failure on the host does not take out the first provider.
