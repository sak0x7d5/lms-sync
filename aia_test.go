package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testCA is a certificate and the key that signs with it.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var testSerial atomic.Int64

func newTestCert(t *testing.T, tmpl *x509.Certificate, parent *testCA) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = big.NewInt(testSerial.Add(1))
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(time.Hour)
	signerCert, signerKey := tmpl, key
	if parent != nil {
		signerCert, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

func newTestRoot(t *testing.T, name string) *testCA {
	return newTestCert(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: name},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, nil)
}

// leafOnlyServer is an LMS that presents its leaf and nothing else — what
// lms.iba.edu.pk does.
func leafOnlyServer(t *testing.T, leaf *testCA) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>portal</html>"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{
		Certificate: [][]byte{leaf.cert.Raw},
		PrivateKey:  leaf.key,
	}}}
	// The refused handshakes are the point of most of these tests.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// clientFor is a Client for https://<host>:<port> whose connections all go
// to srv, trusting only roots. A DNS name rather than 127.0.0.1, because the
// hostname check is half of what is being tested.
func clientFor(t *testing.T, srv *httptest.Server, host string, roots *x509.CertPool) (*Client, string) {
	t.Helper()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	base := "https://" + host + ":" + port
	cfg := DefaultConfig()
	cfg.BaseURL = base
	cfg.Delay = 0
	cfg.Timeout = 10
	c, err := NewClient(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := newTransport(base, false, roots)
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	c.http.Transport = tr
	return c, base
}

// chain builds root → intermediate → leaf for lms.test, the leaf naming
// issuerURL as where its issuer can be fetched.
func chain(t *testing.T, issuerURL string) (root, inter, leaf *testCA) {
	root = newTestRoot(t, "Test Root")
	inter = newTestCert(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: "Test Intermediate"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, root)
	leaf = newTestCert(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: "lms.test"},
		DNSNames:              []string{"lms.test"},
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		IssuingCertificateURL: []string{issuerURL},
	}, inter)
	return root, inter, leaf
}

func TestAnIncompleteChainIsCompletedFromTheLeafsIssuerURL(t *testing.T) {
	// The AIA server has to exist before the leaf naming it, and the
	// intermediate before the AIA server serving it — so serve through a
	// handler that is filled in once both are made.
	var served atomic.Value
	var hits atomic.Int32
	aia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write(served.Load().([]byte))
	}))
	defer aia.Close()

	root, inter, leaf := chain(t, aia.URL+"/inter.crt")
	served.Store(inter.cert.Raw)
	srv := leafOnlyServer(t, leaf)

	roots := x509.NewCertPool()
	roots.AddCert(root.cert)
	c, base := clientFor(t, srv, "lms.test", roots)

	for i := 0; i < 2; i++ {
		body, err := c.getText(context.Background(), base+"/portal")
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if !strings.Contains(body, "portal") {
			t.Fatalf("unexpected body %q", body)
		}
		c.http.CloseIdleConnections() // a new handshake each time
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("intermediate fetched %d times; want once, then cached", n)
	}
}

func TestACompletedChainStillChecksTheHostname(t *testing.T) {
	var served atomic.Value
	aia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(served.Load().([]byte))
	}))
	defer aia.Close()

	root, inter, leaf := chain(t, aia.URL+"/inter.crt")
	served.Store(inter.cert.Raw)
	srv := leafOnlyServer(t, leaf)

	roots := x509.NewCertPool()
	roots.AddCert(root.cert)
	c, base := clientFor(t, srv, "other.test", roots)

	_, err := c.getText(context.Background(), base+"/portal")
	if KindOf(err) != KindTLS {
		t.Fatalf("got %v (kind %v), want a tls error", err, KindOf(err))
	}
	if !strings.Contains(err.Error(), "other.test") {
		t.Fatalf("error does not name the mismatched host: %v", err)
	}
}

func TestAFetchedCertificateIsNeverTrustedAsARoot(t *testing.T) {
	// The leaf's issuer URL serves a self-signed CA this machine does not
	// trust. Accepting it as a root would let anyone on the path to an
	// http:// AIA URL vouch for any server they liked.
	var served atomic.Value
	aia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(served.Load().([]byte))
	}))
	defer aia.Close()

	rogue := newTestRoot(t, "Rogue CA")
	served.Store(rogue.cert.Raw)
	leaf := newTestCert(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: "lms.test"},
		DNSNames:              []string{"lms.test"},
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		IssuingCertificateURL: []string{aia.URL + "/rogue.crt"},
	}, rogue)
	srv := leafOnlyServer(t, leaf)

	trusted := newTestRoot(t, "Trusted Root")
	roots := x509.NewCertPool()
	roots.AddCert(trusted.cert)
	c, base := clientFor(t, srv, "lms.test", roots)

	_, err := c.getText(context.Background(), base+"/portal")
	if KindOf(err) != KindTLS {
		t.Fatalf("got %v (kind %v), want a tls error", err, KindOf(err))
	}
}

func TestAnUnreachableIssuerURLIsStillATLSError(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/inter.crt"
	dead.Close()

	root, _, leaf := chain(t, deadURL)
	srv := leafOnlyServer(t, leaf)
	roots := x509.NewCertPool()
	roots.AddCert(root.cert)
	c, base := clientFor(t, srv, "lms.test", roots)

	_, err := c.getText(context.Background(), base+"/portal")
	if KindOf(err) != KindTLS {
		t.Fatalf("got %v (kind %v), want a tls error", err, KindOf(err))
	}
}
