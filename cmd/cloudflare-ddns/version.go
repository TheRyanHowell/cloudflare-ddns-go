package main

import (
	"context"
	"runtime/debug"
)

// version is set at build time with -ldflags "-X main.version=...". When it is
// empty the module version from the build information is used.
var version string

// fallbackVersion names a build with no version, such as one from a checkout.
const fallbackVersion = "dev"

func resolveVersion(preset string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if preset != "" {
		return preset
	}
	info, ok := readBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return fallbackVersion
	}
	return info.Main.Version
}

func (a app) runVersion(context.Context, func(string) string) error {
	printf(a.stdout, "%s\n", resolveVersion(version, debug.ReadBuildInfo))
	return nil
}
