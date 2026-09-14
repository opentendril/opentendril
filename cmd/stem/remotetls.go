package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const (
	envRemoteListenAddr = "TENDRIL_REMOTE_LISTEN_ADDR"
	envRemoteTLSCert    = "TENDRIL_REMOTE_TLS_CERT"
	envRemoteTLSKey     = "TENDRIL_REMOTE_TLS_KEY"
)

// remoteTLSConfig is an explicitly supplied TLS identity and bind posture for
// a second ingress into this Stem. Its key is independent of the access-token
// signing material and is never derived or generated from it.
type remoteTLSConfig struct {
	listenAddr string
	tlsConfig  *tls.Config
}

// loadRemoteTLSConfig parses and validates the disabled-by-default listener
// configuration. A nil result means all three variables were unset.
func loadRemoteTLSConfig(getenv func(string) string) (*remoteTLSConfig, error) {
	if getenv == nil {
		return nil, fmt.Errorf("remote TLS environment lookup is unavailable")
	}
	listenAddr := strings.TrimSpace(getenv(envRemoteListenAddr))
	certPath := strings.TrimSpace(getenv(envRemoteTLSCert))
	keyPath := strings.TrimSpace(getenv(envRemoteTLSKey))
	if listenAddr == "" && certPath == "" && keyPath == "" {
		return nil, nil
	}
	if listenAddr == "" || certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("remote Stem ingress requires %s, %s, and %s together", envRemoteListenAddr, envRemoteTLSCert, envRemoteTLSKey)
	}
	if err := validateRemoteListenAddr(listenAddr); err != nil {
		return nil, err
	}

	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load remote Stem TLS certificate/key pair: %w", err)
	}
	return &remoteTLSConfig{
		listenAddr: listenAddr,
		tlsConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{certificate},
		},
	}, nil
}

func validateRemoteListenAddr(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid remote Stem listen address %q: expected host:port: %w", address, err)
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("invalid remote Stem listen address %q: host must not contain whitespace", address)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("invalid remote Stem listen address %q: port must be between 1 and 65535", address)
	}
	return nil
}

// prepareRemoteTLSListener binds the configured remote ingress before any
// listener is announced. Bind failure is returned to startup rather than
// silently degrading the Stem to local-only service.
func prepareRemoteTLSListener(config *remoteTLSConfig, handler http.Handler) (net.Listener, *http.Server, error) {
	if config == nil {
		return nil, nil, nil
	}
	if config.tlsConfig == nil || len(config.tlsConfig.Certificates) == 0 {
		return nil, nil, fmt.Errorf("remote Stem ingress has no validated TLS certificate")
	}
	listener, err := net.Listen("tcp", config.listenAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("bind remote Stem TLS listener at %s: %w", config.listenAddr, err)
	}
	return listener, &http.Server{
		Addr:      config.listenAddr,
		Handler:   handler,
		TLSConfig: config.tlsConfig.Clone(),
	}, nil
}

func serveRemoteTLSServer(server *http.Server, listener net.Listener) error {
	if server == nil || listener == nil || server.TLSConfig == nil {
		return fmt.Errorf("remote Stem TLS server is not configured")
	}
	return server.Serve(tls.NewListener(listener, server.TLSConfig))
}
