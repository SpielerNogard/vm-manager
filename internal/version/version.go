// Package version holds the release version, set at build time with -ldflags "-X vm-manager/internal/version.Version=vX.Y.Z".
package version

import "golang.org/x/mod/semver"

var Version = "dev"

func Released(v string) bool { return semver.IsValid(v) && semver.Canonical(v) == v }

func Older(a, b string) bool { return Released(a) && Released(b) && semver.Compare(a, b) < 0 }
