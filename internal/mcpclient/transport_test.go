package mcpclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca testCA) serverCertificate(t *testing.T, dnsNames []string, ipAddresses []net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func startTestTLSServer(t *testing.T, cert tls.Certificate) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func requestWithTransport(endpoint string, transport *http.Transport) error {
	client := &http.Client{Transport: transport, Timeout: time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("unexpected status: " + resp.Status)
	}
	return nil
}

func testSystemRoots(cas ...testCA) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, ca := range cas {
		pool.AddCert(ca.cert)
	}
	return pool
}

func TestReadTrustAnchorPEMRejectsUnreadableAndInvalidMaterial(t *testing.T) {
	if _, err := ReadTrustAnchorPEM(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Fatal("ReadTrustAnchorPEM accepted a missing file")
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTrustAnchorPEM(path); err == nil {
		t.Fatal("ReadTrustAnchorPEM accepted PEM without a valid certificate")
	}
	ca := newTestCA(t)
	path = filepath.Join(t.TempDir(), "trailing-data.pem")
	if err := os.WriteFile(path, append(append([]byte(nil), ca.certPEM...), []byte("unexpected data")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTrustAnchorPEM(path); err == nil {
		t.Fatal("ReadTrustAnchorPEM accepted non-PEM data after a CA certificate")
	}
	leaf := ca.serverCertificate(t, []string{"stem.example.test"}, nil)
	leafPath := filepath.Join(t.TempDir(), "leaf.pem")
	if err := os.WriteFile(leafPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTrustAnchorPEM(leafPath); err == nil {
		t.Fatal("ReadTrustAnchorPEM accepted a server leaf as CA trust material")
	}
}

func TestPrivateTrustAnchorVerifiesNormalTLSIdentity(t *testing.T) {
	ca := newTestCA(t)
	server := startTestTLSServer(t, ca.serverCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")}))
	path := filepath.Join(t.TempDir(), "private-ca.pem")
	if err := os.WriteFile(path, ca.certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := ReadTrustAnchorPEM(path)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := newHTTPTransport(PostureRemoteHTTPS, anchor, func() (*x509.CertPool, error) {
		return x509.NewCertPool(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS client config = %+v; want verification enabled and TLS 1.2 minimum", transport.TLSClientConfig)
	}
	if err := requestWithTransport(server.URL, transport); err != nil {
		t.Fatalf("trusted private CA request: %v", err)
	}
}

func TestUntrustedAuthorityIsRejected(t *testing.T) {
	ca := newTestCA(t)
	server := startTestTLSServer(t, ca.serverCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")}))
	transport, err := newHTTPTransport(PostureRemoteHTTPS, nil, func() (*x509.CertPool, error) {
		return x509.NewCertPool(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	if err := requestWithTransport(server.URL, transport); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("untrusted server request error = %v; want certificate verification failure", err)
	}
}

func TestHostAndIPSANMismatchesAreRejected(t *testing.T) {
	ca := newTestCA(t)
	server := startTestTLSServer(t, ca.serverCertificate(t, []string{"stem.example.test"}, nil))
	transport, err := newHTTPTransport(PostureRemoteHTTPS, ca.certPEM, func() (*x509.CertPool, error) {
		return x509.NewCertPool(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	address := strings.TrimPrefix(server.URL, "https://")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	if err := requestWithTransport("https://wrong.example.test:"+port, transport); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("hostname mismatch error = %v; want SAN verification failure", err)
	}
	if err := requestWithTransport(server.URL, transport); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("IP mismatch error = %v; want IP SAN verification failure", err)
	}
}

func TestCustomTrustAnchorAugmentsSystemRoots(t *testing.T) {
	systemCA := newTestCA(t)
	customCA := newTestCA(t)
	systemServer := startTestTLSServer(t, systemCA.serverCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")}))
	customServer := startTestTLSServer(t, customCA.serverCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")}))
	transport, err := newHTTPTransport(PostureRemoteHTTPS, customCA.certPEM, func() (*x509.CertPool, error) {
		return testSystemRoots(systemCA), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	for _, endpoint := range []string{systemServer.URL, customServer.URL} {
		if err := requestWithTransport(endpoint, transport); err != nil {
			t.Errorf("server rooted by system or custom CA %s failed: %v", endpoint, err)
		}
	}
}

func TestSystemRootLoaderIsUsedWhenNoCustomAnchorIsConfigured(t *testing.T) {
	ca := newTestCA(t)
	server := startTestTLSServer(t, ca.serverCertificate(t, nil, []net.IP{net.ParseIP("127.0.0.1")}))
	loaderCalled := false
	transport, err := newHTTPTransport(PostureRemoteHTTPS, nil, func() (*x509.CertPool, error) {
		loaderCalled = true
		return testSystemRoots(ca), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	if !loaderCalled {
		t.Fatal("system root loader was not used")
	}
	if err := requestWithTransport(server.URL, transport); err != nil {
		t.Fatalf("system-root trusted request: %v", err)
	}
}

func TestClientSetSharesOneTransportAndPreservesTimeoutBounds(t *testing.T) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	clients := NewHTTPClients(transport)
	if clients.Probe.Transport != transport || clients.Mint.Transport != transport || clients.Forward.Transport != transport {
		t.Fatal("probe, mint, and forward clients do not share one transport")
	}
	if clients.Probe.Timeout != readinessTimeout || clients.Mint.Timeout != mintTimeout || clients.Forward.Timeout != forwardingTimeout {
		t.Fatalf("client timeouts = probe %s, mint %s, forward %s", clients.Probe.Timeout, clients.Mint.Timeout, clients.Forward.Timeout)
	}
}

func TestHTTPSUsesStandardEndpointHostnameAsServerName(t *testing.T) {
	posture, err := ValidateGovernedEndpoint("https://stem.example.test:8443")
	if err != nil || posture != PostureRemoteHTTPS {
		t.Fatalf("HTTPS posture = %q, %v", posture, err)
	}
	transport, err := NewHTTPTransport(posture, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.ServerName != "" {
		t.Fatalf("TLS server name override = %q, want Go to verify request host stem.example.test", transport.TLSClientConfig.ServerName)
	}
	if _, err := url.Parse("https://stem.example.test:8443"); err != nil {
		t.Fatal(err)
	}
}
