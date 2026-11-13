package linuxvm

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func parseNBDURL(raw string) (host, port, export string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", err
	}
	host, port, err = net.SplitHostPort(u.Host)
	export = strings.TrimPrefix(u.Path, "/")
	if u.Scheme != "nbd" || err != nil || export == "" {
		return "", "", "", fmt.Errorf("invalid NBD URL %q, want nbd://host:port/export", raw)
	}
	return host, port, export, nil
}

const nbdReconnectSeconds = 7 * 24 * 60 * 60

// qemuNBDArgs connects dev to the export through qemu-nbd, whose NBD driver holds requests while the server is away and resumes them after reconnecting.
func qemuNBDArgs(dev, host, port, export string) []string {
	esc := func(s string) string { return strings.ReplaceAll(s, ",", ",,") }
	return []string{"--connect=" + dev, "--image-opts", fmt.Sprintf(
		"driver=raw,file.driver=nbd,file.server.type=inet,file.server.host=%s,file.server.port=%s,file.export=%s,file.reconnect-delay=%d",
		esc(host), esc(port), esc(export), nbdReconnectSeconds)}
}
