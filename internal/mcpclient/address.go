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

// TransportPosture is the identity contract established by a selected Stem
// endpoint. Callers must use this result rather than reinterpreting schemes or
// hostnames independently.
type TransportPosture string

const (
	PostureLocalLoopbackHTTP TransportPosture = "local-loopback-http"
	PostureRemoteHTTPS       TransportPosture = "remote-https"
)

// ValidateGovernedEndpoint classifies a Stem endpoint's supported Pollinator
// transport posture. Plain HTTP is limited to literal loopback; HTTPS always
// uses normal TLS identity validation, even for a loopback IP.
func ValidateGovernedEndpoint(endpoint string) (TransportPosture, error) {
	u, err := url.Parse(NormalizeEndpoint(endpoint))
	if err != nil {
		return "", fmt.Errorf("transport is not supported: invalid endpoint: %w", err)
	}
	if u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", fmt.Errorf("transport is not supported: endpoint must have a host and contain no credentials")
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		if !isLiteralLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf("transport is not supported: plaintext HTTP requires a literal loopback address (127.0.0.0/8 or ::1)")
		}
		return PostureLocalLoopbackHTTP, nil
	case "https":
		return PostureRemoteHTTPS, nil
	default:
		return "", fmt.Errorf("transport is not supported: use HTTP to literal loopback or HTTPS")
	}
}

// ValidateLocalGovernedEndpoint retains the narrow legacy check for callers
// that explicitly require the local literal-loopback posture.
func ValidateLocalGovernedEndpoint(endpoint string) error {
	posture, err := ValidateGovernedEndpoint(endpoint)
	if err != nil {
		return err
	}
	if posture != PostureLocalLoopbackHTTP {
		return fmt.Errorf("transport is not supported by the local-governed posture: HTTPS has a remote TLS identity contract")
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
