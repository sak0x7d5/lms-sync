package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Completing a chain the server left short.
//
// lms.iba.edu.pk — the DefaultLMS — sends its leaf certificate and nothing
// else, leaving out the GoDaddy intermediate that signed it. Browsers,
// Windows and macOS fetch the missing certificate from the URL the leaf
// names (its Authority Information Access extension); Go on Linux does not,
// so every Linux build, and every phone (Termux runs the linux/arm64 binary),
// failed on the first request with "certificate signed by unknown authority".
// The only advice the tool had was --insecure, which turns verification off
// entirely, for exactly the students least able to judge a network.
//
// So the client does what a browser does. Verification is Go's own
// x509.Verify, against the system roots, with the hostname checked; the one
// thing added is that on an unknown-authority failure the issuer the
// certificate names is fetched and offered as an intermediate. A fetched
// certificate is never a root — it is a candidate link, and the chain still
// has to end at a root this machine already trusts. That is what makes the
// fetch safe over plain HTTP (AIA URLs are http:// by design): a forged
// response can only fail to verify, never verify something that would not.
//
// Bundling GoDaddy's intermediate was the alternative. It is smaller, but it
// fixes one CA at one university until that certificate expires or the
// university changes CA, while the point of the generic name is that this
// is not only IBA's tool.

const (
	aiaMaxHops  = 2                // intermediates fetched per chain
	aiaMaxBytes = 64 << 10         // a certificate is ~1.5 KB; a page is not one
	aiaTimeout  = 10 * time.Second // per fetch; the handshake has no context to cancel it
)

// chainVerifier verifies a server's chain, completing it through AIA when
// the server sent it incomplete.
type chainVerifier struct {
	roots *x509.CertPool // nil: the system's
	fetch *http.Client

	mu    sync.Mutex
	cache map[string]*x509.Certificate // AIA URL → the certificate it served
}

func newChainVerifier(roots *x509.CertPool) *chainVerifier {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &chainVerifier{
		roots: roots,
		cache: map[string]*x509.Certificate{},
		fetch: &http.Client{
			Transport: tr,
			Timeout:   aiaTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("too many redirects")
				}
				if req.URL.Scheme != "http" {
					return errors.New("AIA redirect leaves plain HTTP")
				}
				return nil
			},
		},
	}
}

// newTransport is the LMS client's transport. insecure is --insecure: no
// verification at all, as before. Otherwise a base addressed by a DNS name
// is verified by chainVerifier, and one addressed by an IP literal keeps
// Go's own verification — see verify for why the two cannot share a path.
func newTransport(base string, insecure bool, roots *x509.CertPool) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		return tr
	}
	if u, err := url.Parse(base); err == nil && net.ParseIP(u.Hostname()) != nil {
		if roots != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: roots}
		}
		return tr
	}
	v := newChainVerifier(roots)
	tr.TLSClientConfig = &tls.Config{
		// Only so that VerifyConnection runs instead of Go's verifier, which
		// would abort the handshake before it was called. verify does
		// everything the skipped check does, hostname included.
		InsecureSkipVerify: true,
		VerifyConnection:   v.verify,
	}
	return tr
}

// verify is a tls.Config.VerifyConnection.
//
// The hostname comes from cs.ServerName, which is the SNI the client sent —
// and Go sends none for an IP literal, so for one there is no name to check
// and no way to learn which address was dialled. That is refused rather
// than verified without a hostname; newTransport keeps IP bases off this
// path entirely, so it is only reachable by a redirect to a bare address.
func (v *chainVerifier) verify(cs tls.ConnectionState) error {
	certs := cs.PeerCertificates
	if len(certs) == 0 {
		return errors.New("tls: server sent no certificate")
	}
	if cs.ServerName == "" {
		return &tls.CertificateVerificationError{UnverifiedCertificates: certs,
			Err: errors.New("x509: no server name to verify the certificate against")}
	}

	pool := x509.NewCertPool()
	for _, c := range certs[1:] {
		pool.AddCert(c)
	}
	opts := x509.VerifyOptions{
		DNSName:       cs.ServerName,
		Roots:         v.roots,
		Intermediates: pool,
	}

	_, err := certs[0].Verify(opts)
	var unknown x509.UnknownAuthorityError
	if err == nil || !errors.As(err, &unknown) {
		return wrapVerifyErr(certs, err)
	}

	// Follow the chain from the top of what the server sent: the first
	// certificate whose issuer it did not send.
	top := topOfChain(certs)
	for hop := 0; hop < aiaMaxHops; hop++ {
		issuer, ferr := v.issuerOf(top)
		if ferr != nil {
			// The verification error is the one worth reporting; why the
			// repair failed rides along behind it.
			return wrapVerifyErr(certs, fmt.Errorf("%w (fetching the missing intermediate: %v)", err, ferr))
		}
		if issuer == nil {
			break
		}
		pool.AddCert(issuer) // an intermediate, never a root
		if _, err = certs[0].Verify(opts); err == nil {
			return nil
		}
		if !errors.As(err, &unknown) {
			break
		}
		top = issuer
	}
	return wrapVerifyErr(certs, err)
}

func wrapVerifyErr(certs []*x509.Certificate, err error) error {
	if err == nil {
		return nil
	}
	return &tls.CertificateVerificationError{UnverifiedCertificates: certs, Err: err}
}

// topOfChain walks up from the leaf through the certificates the server
// sent, and returns the highest one it reached.
func topOfChain(certs []*x509.Certificate) *x509.Certificate {
	top := certs[0]
	for i := 0; i < len(certs); i++ {
		next := (*x509.Certificate)(nil)
		for _, c := range certs[1:] {
			if c != top && top.CheckSignatureFrom(c) == nil {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		top = next
	}
	return top
}

// issuerOf fetches the certificate that c's AIA extension says issued it.
// A nil certificate and nil error mean c names no fetchable issuer.
func (v *chainVerifier) issuerOf(c *x509.Certificate) (*x509.Certificate, error) {
	var lastErr error
	for _, raw := range c.IssuingCertificateURL {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" {
			// https would need a verified chain of its own to fetch the
			// thing that completes one; AIA is http by design.
			continue
		}
		v.mu.Lock()
		cached := v.cache[raw]
		v.mu.Unlock()
		if cached != nil {
			return cached, nil
		}
		cert, err := v.download(raw)
		if err != nil {
			lastErr = err
			continue
		}
		v.mu.Lock()
		v.cache[raw] = cert
		v.mu.Unlock()
		return cert, nil
	}
	return nil, lastErr
}

func (v *chainVerifier) download(raw string) (*x509.Certificate, error) {
	resp, err := v.fetch.Get(raw)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %d", raw, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, aiaMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > aiaMaxBytes {
		return nil, fmt.Errorf("%s is larger than a certificate", raw)
	}
	// DER is what AIA URLs serve; PEM turns up often enough to accept.
	if strings.HasPrefix(strings.TrimSpace(string(body)), "-----BEGIN") {
		if block, _ := pem.Decode(body); block != nil && block.Type == "CERTIFICATE" {
			body = block.Bytes
		}
	}
	return x509.ParseCertificate(body)
}
