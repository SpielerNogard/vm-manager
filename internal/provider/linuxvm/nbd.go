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
