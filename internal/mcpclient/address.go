package mcpclient

import (
	"fmt"
	"net"
	"net/url"
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

// ValidateLocalGovernedEndpoint checks the transport posture currently
// qualified for restricted Pollinator credential forwarding. URL origins stay
// location-neutral in configuration so future deployment shapes can be
// represented, but this bridge only presents a durable root to a same-host
// literal loopback Stem over HTTP.
func ValidateLocalGovernedEndpoint(endpoint string) error {
	u, err := url.Parse(NormalizeEndpoint(endpoint))
	if err != nil {
		return fmt.Errorf("transport is not supported by the current local-governed posture: invalid endpoint: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") || !isLiteralLoopbackHost(u.Hostname()) {
		return fmt.Errorf("transport is not supported by the current local-governed posture: durable Pollinator credentials are only forwarded over http to a literal loopback address (127.0.0.0/8 or ::1)")
	}
	return nil
}

func isLiteralLoopbackHost(host string) bool {
	if host == "::1" {
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	ip := net.ParseIP(host).To4()
	return ip != nil && ip[0] == 127
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
