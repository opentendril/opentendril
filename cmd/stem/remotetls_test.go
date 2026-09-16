package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

type remoteTestCertificate struct {
	certificatePath string
	keyPath         string
	certificate     *x509.Certificate
	keyPEM          []byte
}

func writeRemoteTestCertificate(t *testing.T, dir string) remoteTestCertificate {
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
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "remote-cert.pem")
	keyPath := filepath.Join(dir, "remote-key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return remoteTestCertificate{certificatePath: certPath, keyPath: keyPath, certificate: certificate, keyPEM: keyPEM}
}

func remoteTestEnvironment(address, certPath, keyPath string) func(string) string {
	values := map[string]string{
		envRemoteListenAddr: address,
		envRemoteTLSCert:    certPath,
		envRemoteTLSKey:     keyPath,
	}
	return func(name string) string { return values[name] }
}

func reserveRemoteTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestLoadRemoteTLSConfigDisabledAndPartialCombinations(t *testing.T) {
	config, err := loadRemoteTLSConfig(func(string) string { return "" })
	if err != nil || config != nil {
		t.Fatalf("unset remote configuration = %+v, %v; want disabled", config, err)
	}

	fields := []string{envRemoteListenAddr, envRemoteTLSCert, envRemoteTLSKey}
	for mask := 1; mask < 1<<len(fields); mask++ {
		if mask == (1<<len(fields))-1 {
			continue
		}
		t.Run(strconv.Itoa(mask), func(t *testing.T) {
			values := map[string]string{}
			for index, name := range fields {
				if mask&(1<<index) != 0 {
					values[name] = "configured"
				}
			}
			if config, err := loadRemoteTLSConfig(func(name string) string { return values[name] }); err == nil || config != nil {
				t.Fatalf("partial configuration = %+v, %v; want fail-closed error", config, err)
			}
		})
	}
}

func TestLoadRemoteTLSConfigRejectsInvalidAddressAndCertificatePair(t *testing.T) {
	dir := t.TempDir()
	pair := writeRemoteTestCertificate(t, dir)
	for _, address := range []string{"", "stem.example", "127.0.0.1:0", "127.0.0.1:not-a-port"} {
		if _, err := loadRemoteTLSConfig(remoteTestEnvironment(address, pair.certificatePath, pair.keyPath)); err == nil {
			t.Errorf("loadRemoteTLSConfig address %q succeeded; want refusal", address)
		}
	}
	other := writeRemoteTestCertificate(t, t.TempDir())
	if _, err := loadRemoteTLSConfig(remoteTestEnvironment("127.0.0.1:8443", pair.certificatePath, other.keyPath)); err == nil {
		t.Fatal("loadRemoteTLSConfig accepted a certificate and unrelated private key")
	}
	invalid := filepath.Join(dir, "invalid.pem")
	if err := os.WriteFile(invalid, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRemoteTLSConfig(remoteTestEnvironment("127.0.0.1:8443", invalid, pair.keyPath)); err == nil {
		t.Fatal("loadRemoteTLSConfig accepted malformed certificate material")
	}
}

func TestLoadRemoteTLSConfigLoadsValidPairAndRequiresTLS12(t *testing.T) {
	pair := writeRemoteTestCertificate(t, t.TempDir())
	config, err := loadRemoteTLSConfig(remoteTestEnvironment("127.0.0.1:8443", pair.certificatePath, pair.keyPath))
	if err != nil {
		t.Fatalf("loadRemoteTLSConfig: %v", err)
	}
	if config == nil || config.listenAddr != "127.0.0.1:8443" {
		t.Fatalf("remote config = %+v", config)
	}
	if config.tlsConfig.MinVersion != tls.VersionTLS12 || len(config.tlsConfig.Certificates) != 1 {
		t.Fatalf("TLS config = %+v; want one loaded certificate and TLS 1.2 minimum", config.tlsConfig)
	}
}

func TestPreparedRemoteListenerEnforcesTLS12(t *testing.T) {
	pair := writeRemoteTestCertificate(t, t.TempDir())
	config, err := loadRemoteTLSConfig(remoteTestEnvironment(reserveRemoteTestAddress(t), pair.certificatePath, pair.keyPath))
	if err != nil {
		t.Fatal(err)
	}
	listener, server, err := prepareRemoteTLSListener(config, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if err != nil {
		t.Fatalf("prepareRemoteTLSListener: %v", err)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.IdleTimeout != 60*time.Second || server.MaxHeaderBytes != 32<<10 {
		t.Fatalf("remote server limits = header %s, idle %s, max headers %d; want 5s, 60s, 32 KiB", server.ReadHeaderTimeout, server.IdleTimeout, server.MaxHeaderBytes)
	}
	if server.ReadTimeout != 0 || server.WriteTimeout != 0 {
		t.Fatalf("remote global timeouts = read %s, write %s; want unset", server.ReadTimeout, server.WriteTimeout)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveRemoteTLSServer(server, listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		<-serveDone
	})

	roots := x509.NewCertPool()
	roots.AddCert(pair.certificate)
	dialer := &net.Dialer{Timeout: time.Second}
	if conn, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), &tls.Config{
		MinVersion: tls.VersionTLS10,
		MaxVersion: tls.VersionTLS11,
		RootCAs:    roots,
		ServerName: "127.0.0.1",
	}); err == nil {
		_ = conn.Close()
		t.Fatal("remote listener accepted a TLS version below 1.2")
	}
	conn, err := tls.DialWithDialer(dialer, "tcp", listener.Addr().String(), &tls.Config{
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
		RootCAs:    roots,
		ServerName: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("TLS 1.2 handshake: %v", err)
	}
	_ = conn.Close()
}

func TestPreparedRemoteListenerRejectsOversizedHeaders(t *testing.T) {
	pair := writeRemoteTestCertificate(t, t.TempDir())
	config, err := loadRemoteTLSConfig(remoteTestEnvironment(reserveRemoteTestAddress(t), pair.certificatePath, pair.keyPath))
	if err != nil {
		t.Fatal(err)
	}
	listener, server, err := prepareRemoteTLSListener(config, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("prepareRemoteTLSListener: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveRemoteTLSServer(server, listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		<-serveDone
	})

	roots := x509.NewCertPool()
	roots.AddCert(pair.certificate)
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "127.0.0.1"})
	if err != nil {
		t.Fatalf("TLS connection: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	request := "GET /health HTTP/1.1\r\nHost: localhost\r\nX-Oversized: " + strings.Repeat("x", (32<<10)+8192) + "\r\nConnection: close\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatalf("write oversized-header request: %v", err)
	}
	statusLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read oversized-header response: %v", err)
	}
	if !strings.Contains(statusLine, " 431 ") {
		t.Fatalf("oversized-header status line = %q, want 431", statusLine)
	}
}

func TestConnectionLimitedListenerCapsAndReleasesExactlyOnce(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	limited := newConnectionLimitedListener(listener, 1)
	t.Cleanup(func() { _ = limited.Close() })
	type acceptResult struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptResult, 2)
	go func() {
		conn, err := limited.Accept()
		accepted <- acceptResult{conn: conn, err: err}
	}()

	firstClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer firstClient.Close()
	var first acceptResult
	select {
	case first = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("first connection was not accepted")
	}
	if first.err != nil {
		t.Fatal(first.err)
	}
	if first.conn == nil {
		t.Fatal("first accepted connection is nil")
	}

	go func() {
		conn, err := limited.Accept()
		accepted <- acceptResult{conn: conn, err: err}
	}()
	secondClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer secondClient.Close()
	_ = secondClient.SetReadDeadline(time.Now().Add(time.Second))
	var probe [1]byte
	if _, err := secondClient.Read(probe[:]); err == nil {
		t.Fatal("connection above the configured cap remained open")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("connection above the configured cap was left waiting instead of being refused")
	}
	select {
	case extra := <-accepted:
		if extra.conn != nil {
			_ = extra.conn.Close()
		}
		t.Fatal("listener returned more than one active connection")
	default:
	}

	if err := first.conn.Close(); err != nil {
		t.Fatalf("close first accepted connection: %v", err)
	}
	if err := first.conn.Close(); err == nil {
		t.Fatal("second close unexpectedly succeeded")
	}

	thirdClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer thirdClient.Close()
	var third acceptResult
	select {
	case third = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("capacity was not released after connection close")
	}
	if third.err != nil || third.conn == nil {
		t.Fatalf("connection after release = %v, %v", third.conn, third.err)
	}
	defer third.conn.Close()

	go func() {
		conn, err := limited.Accept()
		accepted <- acceptResult{conn: conn, err: err}
	}()
	fourthClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer fourthClient.Close()
	_ = fourthClient.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := fourthClient.Read(probe[:]); err == nil {
		t.Fatal("duplicate close released the active connection's slot")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("connection above the cap remained waiting instead of being refused")
	}
}

func TestRemoteConnectionCapRejectsBeforeTLS(t *testing.T) {
	pair := writeRemoteTestCertificate(t, t.TempDir())
	config, err := loadRemoteTLSConfig(remoteTestEnvironment(reserveRemoteTestAddress(t), pair.certificatePath, pair.keyPath))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	listener, server, err := prepareRemoteTLSListenerWithConnectionLimit(config, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), 1)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveRemoteTLSServer(server, listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		<-serveDone
	})

	roots := x509.NewCertPool()
	roots.AddCert(pair.certificate)
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "127.0.0.1"}
	first, err := tls.Dial("tcp", listener.Addr().String(), tlsConfig.Clone())
	if err != nil {
		t.Fatalf("first TLS connection: %v", err)
	}
	defer first.Close()

	second, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetDeadline(time.Now().Add(time.Second))
	_, _ = second.Write([]byte("not a TLS record"))
	bytesReceived, err := io.Copy(io.Discard, second)
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatal("over-cap raw connection waited for TLS processing instead of being refused")
	}
	if bytesReceived != 0 {
		t.Fatalf("over-cap raw connection received %d TLS response bytes; rejection happened after TLS processing", bytesReceived)
	}
	if requests.Load() != 0 {
		t.Fatalf("over-cap connection reached HTTP handler %d times", requests.Load())
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first TLS connection: %v", err)
	}
}

func TestPreparedRemoteListenerBindFailureIsReturned(t *testing.T) {
	pair := writeRemoteTestCertificate(t, t.TempDir())
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	config, err := loadRemoteTLSConfig(remoteTestEnvironment(occupied.Addr().String(), pair.certificatePath, pair.keyPath))
	if err != nil {
		t.Fatal(err)
	}
	if listener, server, err := prepareRemoteTLSListener(config, http.NotFoundHandler()); err == nil {
		if listener != nil {
			_ = listener.Close()
		}
		if server != nil {
			_ = server.Close()
		}
		t.Fatal("prepareRemoteTLSListener silently accepted an occupied address")
	}
}

func TestRemoteListenerSetupBindsOnlyItsConfiguredAddress(t *testing.T) {
	pair := writeRemoteTestCertificate(t, t.TempDir())
	remoteAddress := reserveRemoteTestAddress(t)
	gatewayAddress := reserveRemoteTestAddress(t)
	_, gatewayPort, err := net.SplitHostPort(gatewayAddress)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_PORT", gatewayPort)
	config, err := loadRemoteTLSConfig(remoteTestEnvironment(remoteAddress, pair.certificatePath, pair.keyPath))
	if err != nil {
		t.Fatal(err)
	}
	listener, server, err := prepareRemoteTLSListener(config, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer server.Close()
	if got := listener.Addr().String(); got != remoteAddress || server.Addr != remoteAddress {
		t.Fatalf("remote listener address = %q / %q, want configured remote address %q", got, server.Addr, remoteAddress)
	}
	gatewayListener, err := net.Listen("tcp", gatewayAddress)
	if err != nil {
		t.Fatalf("remote listener setup occupied standalone Gateway address %s: %v", gatewayAddress, err)
	}
	_ = gatewayListener.Close()
}

func TestRemoteMuxSharesCoreAuthorityAndIgnoresForwardingHeaders(t *testing.T) {
	var executions int
	var invokedPollen string
	coreSvc := core.NewService(nil).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, _ core.SproutSpec) (core.SproutRunReport, error) {
			executions++
			invokedPollen = core.PollenFromContext(ctx)
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})
	fixture := newRemoteMuxTestFixture(t, "grants:\n  claude:\n    operationClasses: [sprout.grow]\n    substrates: [core]\n", coreSvc)
	root := fixture.root
	signer := fixture.signer
	remoteMux := fixture.mux
	localMux := fixture.localMux()

	mint := httptest.NewRecorder()
	mintRequest := httptest.NewRequest(http.MethodPost, "/v1/pollinator/token", nil)
	mintRequest.Header.Set("Authorization", "Bearer "+root)
	remoteMux.ServeHTTP(mint, mintRequest)
	if mint.Code != http.StatusOK {
		t.Fatalf("remote root mint status = %d, want 200 (%s)", mint.Code, mint.Body.String())
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(mint.Body.Bytes(), &response); err != nil || response.Token == "" {
		t.Fatalf("remote mint token = %q, decode error %v", response.Token, err)
	}
	if claims, ok := signer.VerifyAccessToken(response.Token); !ok || claims.Pollen != "claude" {
		t.Fatalf("minted token claims = %+v, valid=%v", claims, ok)
	}

	request := func(mux http.Handler, path, bearer, body string) *httptest.ResponseRecorder {
		return serveMuxRequest(mux, http.MethodPost, path, body, map[string]string{
			"Authorization":        "Bearer " + bearer,
			"Forwarded":            "for=127.0.0.1;host=localhost;proto=http",
			"X-Forwarded-For":      "127.0.0.1",
			"X-Forwarded-Host":     "localhost",
			"X-Forwarded-Proto":    "http",
			receptors.PollenHeader: "attacker-pollen",
		})
	}
	mcpGrow := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sproutGrow","arguments":{"transcript":"grow","substrate":"core"}}}`
	for _, path := range []string{"/v1"} {
		if got := request(remoteMux, path, root, mcpGrow).Code; got != http.StatusUnauthorized {
			t.Errorf("remote durable root on %s with forwarded headers = %d, want 401", path, got)
		}
	}
	if got := request(remoteMux, "/v1", response.Token, mcpGrow).Code; got != http.StatusOK {
		t.Fatalf("remote access-token governed route = %d, want 200", got)
	}
	if invokedPollen != "claude" {
		t.Fatalf("public Core Pollen = %q, want token-derived claude", invokedPollen)
	}
	if got := request(localMux, "/v1/sprouts/grow", root, `{"transcript":"grow","substrate":"core"}`).Code; got != http.StatusOK {
		t.Fatalf("existing local root-on-data route = %d, want 200", got)
	}
	if executions != 2 {
		t.Fatalf("Core sprout executions = %d, want remote access token + existing local root", executions)
	}
}
