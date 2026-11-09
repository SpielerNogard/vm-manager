// Package web embeds the built Vue GUI (run `make web` first).
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
