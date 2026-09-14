package mcpclient

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

const (
	readinessTimeout = 2 * time.Second
	mintTimeout      = 10 * time.Second
)

// HTTPClients applies operation-specific bounds while sharing one underlying
// transport, including its TLS trust configuration.
type HTTPClients struct {
	Probe   *http.Client
	Mint    *http.Client
	Forward *http.Client
}

// NewHTTPClients creates short readiness and mint wrappers and the existing
// long-running forwarding wrapper over one transport.
func NewHTTPClients(transport http.RoundTripper) HTTPClients {
	return HTTPClients{
		Probe:   &http.Client{Transport: transport, Timeout: readinessTimeout},
		Mint:    &http.Client{Transport: transport, Timeout: mintTimeout},
		Forward: &http.Client{Transport: transport, Timeout: forwardingTimeout},
	}
}

// ReadTrustAnchorPEM reads and validates named public CA material after its
// caller has resolved a reference through pollinatorconfig.
func ReadTrustAnchorPEM(path string) ([]byte, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trust anchor %s: %w", path, err)
	}
	if err := validateTrustAnchorPEM(contents); err != nil {
		return nil, fmt.Errorf("trust anchor %s: %w", path, err)
	}
	return contents, nil
}

func validateTrustAnchorPEM(contents []byte) error {
	rest := contents
	foundCA := false
	for len(rest) > 0 {
		rest = bytes.TrimLeft(rest, " \t\r\n")
		if len(rest) == 0 {
			break
		}
		if !bytes.HasPrefix(rest, []byte("-----BEGIN ")) {
			return errors.New("contains non-PEM data")
		}
		block, remaining := pem.Decode(rest)
		if block == nil {
			return errors.New("contains malformed PEM data")
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("contains an invalid certificate: %w", err)
		}
		if !certificate.IsCA || !certificate.BasicConstraintsValid {
			return errors.New("contains a certificate that is not a valid CA")
		}
		foundCA = true
	}
	if !foundCA {
		return errors.New("contains no valid PEM CA certificate")
	}
	return nil
}

// NewHTTPTransport constructs the selected endpoint's HTTP transport. HTTPS
// uses system roots and appends optional PEM CA material while leaving Go's
// normal chain and hostname/IP verification enabled.
func NewHTTPTransport(posture TransportPosture, trustAnchorPEM []byte) (*http.Transport, error) {
	return newHTTPTransport(posture, trustAnchorPEM, x509.SystemCertPool)
}

func newHTTPTransport(posture TransportPosture, trustAnchorPEM []byte, loadSystemRoots func() (*x509.CertPool, error)) (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("default HTTP transport has an unsupported implementation")
	}
	transport := base.Clone()
	if posture == PostureLocalLoopbackHTTP {
		return transport, nil
	}
	if posture != PostureRemoteHTTPS {
		return nil, fmt.Errorf("cannot construct HTTP transport for posture %q", posture)
	}
	if loadSystemRoots == nil {
		return nil, errors.New("system certificate root loader is unavailable")
	}
	roots, err := loadSystemRoots()
	if err != nil {
		return nil, fmt.Errorf("load system certificate roots: %w", err)
	}
	if roots == nil {
		return nil, errors.New("system certificate roots are unavailable")
	}
	roots = roots.Clone()
	if len(trustAnchorPEM) != 0 {
		if err := validateTrustAnchorPEM(trustAnchorPEM); err != nil {
			return nil, fmt.Errorf("configured trust anchor: %w", err)
		}
		if !roots.AppendCertsFromPEM(trustAnchorPEM) {
			return nil, errors.New("configured trust anchor contains no valid PEM CA certificate")
		}
	}
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	return transport, nil
}
