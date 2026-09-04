package mcpclient

import (
	"net"
	"os"
	"strings"
)

// NormalizeEndpoint removes the optional root slash from a validated endpoint
// before a transport client composes API paths. Validation belongs to the
// Pollinator configuration package; this helper only handles the transport
// boundary and remains useful to callers with an already validated origin.
func NormalizeEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

// ResolveStemAddress returns host:port for the Stem this client should
// contact. TERROIR_HOST wins when set; otherwise fallbackHost, otherwise
// 127.0.0.1. A TERROIR_HOST value that already includes a port contributes
// only its host. PORT defaults to 8080.
func ResolveStemAddress(fallbackHost string) string {
	host := strings.TrimSpace(os.Getenv("TERROIR_HOST"))
	if host == "" {
		if fallbackHost != "" {
			host = fallbackHost
		} else {
			host = "127.0.0.1"
		}
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	return net.JoinHostPort(host, port)
}
