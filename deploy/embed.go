// Package deploy carries the compose project of a hub that runs on the host.
package deploy

import "embed"

//go:embed stack.compose.yaml gateway.Dockerfile storage.Dockerfile gateway-start.sh
var Stack embed.FS
