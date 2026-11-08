# Configuration

Every setting can come from an environment variable or a command-line flag. The [README](../README.md#configuration) lists them all.

## Flags and the environment

- A flag you set wins over its environment variable.
- A flag left empty or blank, such as an unset shell variable, falls back to the environment.
- Setting `--token` or `--token-file` makes the command ignore every token variable, so a flag replaces the environment's token instead of clashing with it.
- Values go through the same validation either way. Errors describe the setting, such as `record TTL`, rather than a flag or variable.

`--token` puts the secret in the process list, where other users on the machine can read it. Prefer `--token-file` or the environment.

## The API token

Create an API token in the Cloudflare dashboard with **Zone → DNS → Edit** permission, limited to the one zone. Give it either directly (`CLOUDFLARE_API_TOKEN`) or as a file (`CLOUDFLARE_API_TOKEN_FILE`), not both.

To keep the token out of `.env`, store it as a Docker secret and point `CLOUDFLARE_API_TOKEN_FILE` at it. The container runs as UID 65532, so the file must be readable by that user:

```yaml
services:
  app:
    environment:
      CLOUDFLARE_API_TOKEN_FILE: /run/secrets/cloudflare_token
    secrets:
      - cloudflare_token
secrets:
  cloudflare_token:
    file: ./cloudflare_token.txt
```

## Finding the record ID

The zone ID is on the zone's Overview page in the Cloudflare dashboard. To find the record ID, list the zone's `A` records and copy the `id` of the one to manage:

```sh
curl -s -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/zones/$CLOUDFLARE_ZONE_ID/dns_records?type=A"
```

`CLOUDFLARE_RECORD_NAME` must be that record's full name. The daemon refuses a record ID that belongs to a different name, so a typo cannot rename another record.
