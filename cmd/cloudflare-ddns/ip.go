package main

import (
	"context"
	"fmt"
)

// runIP prints the public IP address and needs no Cloudflare settings.
func (a app) runIP(ctx context.Context, getenv func(string) string) error {
	providers, err := ipProviderList(getenv)
	if err != nil {
		return err
	}
	lookup, err := a.newLookup(providers, defaultSyncInterval)
	if err != nil {
		return err
	}
	ip, err := lookup.Current(ctx)
	if err != nil {
		return fmt.Errorf("look up public IP: %w", err)
	}
	printf(a.stdout, "%s\n", ip)
	return nil
}
