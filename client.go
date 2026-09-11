package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/125.0 Safari/537.36 lms-sync/" + version

var (
	loginFormRe = regexp.MustCompile(`(?i)name=["']?(eid|pw)["']?`)
	loggedInRe  = regexp.MustCompile(`(?i)(portal/logout|/logout|log\s*out|my\s*workspace)`)
)

// Client is an authenticated Sakai session.
type Client struct {
	base       string
	http       *http.Client
	delay      time.Duration
	retries    int
	loginPaths []string
}

func NewClient(cfg *Config, insecure bool) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, failf(KindUnknown, "create cookie jar", "", err)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	paths := []string{"/access/login", "/portal/xlogin", "/portal/relogin"}
	if cfg.LoginPath != "" {
		paths = []string{cfg.LoginPath}
	}

	return &Client{
		base: strings.TrimRight(cfg.BaseURL, "/"),
		http: &http.Client{
			Jar:       jar,
			Transport: transport,
			Timeout:   time.Duration(cfg.Timeout) * time.Second,
		},
		delay:      time.Duration(cfg.Delay) * time.Millisecond,
		retries:    cfg.Retries,
		loginPaths: paths,
	}, nil
}

// do performs a request, retrying only what is worth retrying.
//
// Timeouts, connection drops, 429 and 5xx are retried with exponential
// backoff and honour Retry-After. 4xx are answers, not failures — retrying
// a rejected login is how accounts get locked.
func (c *Client) do(ctx context.Context, method, rawURL string, body io.Reader,
	headers map[string]string) (*http.Response, error) {

	var lastErr error

	for attempt := 1; attempt <= c.retries; attempt++ {
		if err := c.pause(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return nil, failf(KindConfig, method+" "+rawURL,
				"That address doesn't look valid.", err)
		}
		req.Header.Set("User-Agent", userAgent)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctxErr(ctx, method+" "+shortURL(rawURL))
			}
			if isTLSError(err) {
				return nil, failf(KindTLS, method+" "+shortURL(rawURL), hintTLS, err)
			}
			lastErr = failf(KindNetwork, method+" "+shortURL(rawURL), hintNetwork, err)
			// A body can only be read once, so a retry would send nothing.
			// See the branch below, which has to make the same check.
			if body != nil {
				return nil, lastErr
			}
		} else if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			wait := retryAfter(resp)
			resp.Body.Close()
			lastErr = failf(KindServer,
				fmt.Sprintf("%s %s returned %d", method, shortURL(rawURL), resp.StatusCode),
				hintServer, nil)
			// Same rule as the branch above, and it has to be stated in both:
			// the reader was drained by the attempt that just failed, so a
			// retry would POST an empty form. On the login endpoint that is
			// not merely a wasted round trip — the server records it as a
			// sign-in attempt with no password, against the one endpoint
			// where repeated failures lock an account.
			if body != nil {
				return nil, lastErr
			}
			if wait > 0 && attempt < c.retries {
				if err := sleepCtx(ctx, wait); err != nil {
					return nil, err
				}
				continue
			}
		} else {
			return resp, nil
		}

		if attempt < c.retries {
			backoff := time.Duration(1<<attempt) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, err
			}
		}
	}

	// Never return a nil response with a nil error. Every caller here goes
	// straight to resp.Body, so that pair is a panic rather than a failure —
	// and it is reachable whenever retries is below 1, which is the value a
	// Config built in code rather than loaded through sanitise() carries.
	if lastErr == nil {
		lastErr = failf(KindConfig, method+" "+shortURL(rawURL),
			"No request was attempted. Check that \"retries\" is at least 1.", nil)
	}
	return nil, lastErr
}

func (c *Client) pause(ctx context.Context) error {
	if c.delay <= 0 {
		return nil
	}
	return sleepCtx(ctx, c.delay)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctxErr(ctx, "the request")
	case <-t.C:
		return nil
	}
}

func retryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			if secs > 60 {
				secs = 60
			}
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func isTLSError(err error) bool {
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "certificate") || strings.Contains(msg, "x509")
}

func shortURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	return raw
}

// getText fetches a URL and returns the body as a string.
func (c *Client) getText(ctx context.Context, rawURL string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, rawURL, nil, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", failf(KindNotFound,
			fmt.Sprintf("GET %s returned %d", shortURL(rawURL), resp.StatusCode),
			"", nil)
	}
	// Course pages are HTML; cap the read so a misconfigured server can't
	// exhaust memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", failf(KindNetwork, "read "+shortURL(rawURL),
			"The connection dropped mid-response.", err)
	}
	return string(body), nil
}

// Login authenticates, trying each candidate endpoint.
func (c *Client) Login(ctx context.Context, username, password string) error {
	// Touch the portal first so Sakai issues a session cookie to bind to.
	if _, err := c.getText(ctx, c.base+"/portal"); err != nil {
		if KindOf(err) == KindNotFound {
			return failf(KindNetwork, "reach "+c.base,
				"The address responded, but not like a Sakai portal.\n"+
					"Check base_url points at the LMS root, not a course page.", err)
		}
		return err
	}

	form := url.Values{"eid": {username}, "pw": {password}, "submit": {"Log in"}}
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Referer":      c.base + "/portal",
	}

	var lastErr error
	for _, path := range c.loginPaths {
		resp, err := c.do(ctx, http.MethodPost, c.base+path,
			strings.NewReader(form.Encode()), headers)
		if err != nil {
			if KindOf(err) == KindCancelled || KindOf(err) == KindTLS {
				return err
			}
			lastErr = err
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		// Sakai answers a wrong password with HTTP 200 and the login form
		// again, so the status code proves nothing. Check the session.
		ok, err := c.Authenticated(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		if ok {
			return nil
		}
	}

	if lastErr != nil && KindOf(lastErr) != KindNotFound {
		return lastErr
	}
	return failf(KindAuth, "log in as "+username, hintAuth, nil)
}

// Authenticated uses two independent signals, because Sakai deployments vary.
//
// The Entity Broker (/direct) is the clean answer but is disabled on many
// installs, so its absence proves nothing. What the portal renders is the
// fallback — and that fallback is what stops a working login from being
// reported as a bad password.
func (c *Client) Authenticated(ctx context.Context) (bool, error) {
	if body, err := c.getText(ctx, c.base+"/direct/session/current.json"); err == nil {
		if strings.Contains(body, `"userId"`) && !strings.Contains(body, `"userId":""`) {
			return true, nil
		}
	} else if k := KindOf(err); k == KindCancelled || k == KindTLS || k == KindNetwork {
		return false, err
	}

	body, err := c.getText(ctx, c.base+"/portal")
	if err != nil {
		return false, err
	}
	return loggedInRe.MatchString(body) && !loginFormRe.MatchString(body), nil
}
