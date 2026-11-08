# Using the packages

The module `github.com/TheRyanHowell/cloudflare-ddns-go` has three importable packages:

| Package | Use it to |
|---|---|
| `publicip` | Look up the public IPv4 address across the providers, with fallback and cooldowns |
| `cloudflare` | Read and update one Cloudflare DNS record |
| `ddns` | Keep a record in step with the public IP: once with `Sync`, or continuously with `Run` |

This looks up the public IP, then brings a record in step with it once:

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/TheRyanHowell/cloudflare-ddns-go/cloudflare"
	"github.com/TheRyanHowell/cloudflare-ddns-go/ddns"
	"github.com/TheRyanHowell/cloudflare-ddns-go/publicip"
)

func main() {
	ctx := context.Background()

	lookup, err := publicip.New(publicip.Providers())
	if err != nil {
		log.Fatal(err)
	}
	ip, err := lookup.Current(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("public IP:", ip)

	record := cloudflare.NewRecord(cloudflare.RecordConfig{
		Token:    "API_TOKEN",
		ZoneID:   "ZONE_ID",
		RecordID: "RECORD_ID",
		Name:     "home.example.com",
		TTL:      300,
	})
	syncer := ddns.New(record, lookup, ddns.WithPermanentCheck(cloudflare.IsPermanent))
	ip, changed, err := syncer.Sync(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("record is", ip, "updated:", changed)
}
```

`Sync` wraps errors that retrying cannot fix, such as a rejected token, with `ddns.ErrPermanent`. Options given a nil value, a non-positive interval or a negative delay keep their defaults.

The API may change before version 1.0. See [architecture](architecture.md) for how the packages fit together.
