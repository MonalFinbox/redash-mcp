// Package policy is the only package in this program permitted to construct
// an HTTP request. Everything the server can reach is described by the
// endpoint table in endpoints.go, and every outbound call funnels through
// Guard, so the read-only property can be checked by reading one directory.
//
// An architecture test enforces the "only package" claim against the import
// graph, and a transport-level test asserts that no non-GET request is ever
// issued.
package policy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const userAgent = "redash-mcp (+https://github.com/MonalFinbox/redash-mcp)"

var (
	ErrUnregistered = errors.New("endpoint is not in the policy table")
	ErrTierDenied   = errors.New("endpoint requires a higher capability tier than this server was started with")
	ErrNotGet       = errors.New("Get called with a non-GET endpoint")
	ErrRedirect     = errors.New("Redash returned a redirect, which is not followed")
	ErrTooLarge     = errors.New("Redash response exceeded the size cap")
)

// Target is a resolved Redash instance. Tools never build one: they name an
// instance, and configuration hands the guard the matching Target. This is
// what keeps a destination URL from ever being a model-supplied value.
type Target struct {
	// Name is the short instance key, e.g. "prod".
	Name string

	// EnforcedReadOnly records whether the Redash account behind this key is
	// itself incapable of writing, because it sits in a View Only group.
	// It is reported in the startup banner and the audit log; the guard
	// never relaxes anything on the strength of it.
	EnforcedReadOnly bool

	baseURL *url.URL
	apiKey  string
}

func NewTarget(name string, base *url.URL, apiKey string, enforcedReadOnly bool) Target {
	return Target{Name: name, EnforcedReadOnly: enforcedReadOnly, baseURL: base, apiKey: apiKey}
}

func (t Target) BaseURL() string {
	if t.baseURL == nil {
		return ""
	}
	return t.baseURL.String()
}

// Guard owns the process's only HTTP client.
type Guard struct {
	tier     Tier
	maxBytes int64
	client   *http.Client
}

type Options struct {
	Tier         Tier
	Timeout      time.Duration
	MaxBodyBytes int64

	// Transport is a test seam. Production callers leave it nil.
	Transport http.RoundTripper
}

func NewGuard(o Options) *Guard {
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 8 << 20
	}
	tr := o.Transport
	if tr == nil {
		tr = &http.Transport{
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
			Proxy:             http.ProxyFromEnvironment,
		}
	}
	return &Guard{
		tier:     o.Tier,
		maxBytes: o.MaxBodyBytes,
		client: &http.Client{
			Transport: tr,
			Timeout:   o.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return ErrRedirect
			},
		},
	}
}

func (g *Guard) Tier() Tier { return g.tier }

// Binding supplies typed values for an endpoint's path parameters. Because
// an id is an int64 and a slug must match slugPattern, path traversal and
// query-string smuggling are unrepresentable rather than filtered out.
type Binding struct {
	ID   int64
	Slug string
}

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func bindPath(tmpl string, b Binding) (string, error) {
	out := tmpl
	if strings.Contains(out, "{id}") {
		if b.ID <= 0 {
			return "", fmt.Errorf("path %q needs a positive id, got %d", tmpl, b.ID)
		}
		out = strings.ReplaceAll(out, "{id}", strconv.FormatInt(b.ID, 10))
	}
	if strings.Contains(out, "{slug}") {
		if !slugPattern.MatchString(b.Slug) {
			return "", fmt.Errorf("%q is not a valid Redash slug", b.Slug)
		}
		out = strings.ReplaceAll(out, "{slug}", b.Slug)
	}
	if strings.ContainsAny(out, "{}") {
		return "", fmt.Errorf("unbound path parameter in %q", tmpl)
	}
	return out, nil
}

// Get issues the one kind of request this program knows how to make.
//
// There is deliberately no Do, Post, or Delete. Adding one would make the
// non-GET transport test fail, which is the intended tripwire.
func (g *Guard) Get(ctx context.Context, t Target, ep Endpoint, b Binding, q url.Values) ([]byte, error) {
	if !isRegistered(ep) {
		return nil, fmt.Errorf("%w: %q", ErrUnregistered, ep.name)
	}
	if ep.tier > g.tier {
		return nil, fmt.Errorf("%w: %s needs tier %s, server started at tier %s",
			ErrTierDenied, ep.name, ep.tier, g.tier)
	}
	if ep.method != http.MethodGet {
		return nil, fmt.Errorf("%w: %s is %s", ErrNotGet, ep.name, ep.method)
	}
	if t.baseURL == nil {
		return nil, fmt.Errorf("instance %q has no base URL", t.Name)
	}

	p, err := bindPath(ep.path, b)
	if err != nil {
		return nil, err
	}

	u := *t.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + p
	u.RawQuery = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	// The key travels in a header and never in the URL, so it stays out of
	// Redash access logs, proxy logs, and any error that quotes the URL.
	req.Header.Set("Authorization", "Key "+t.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, networkError(t, err)
	}
	defer resp.Body.Close()

	// One byte past the cap is read so an oversized body can be told apart
	// from one that is exactly at it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, g.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response from instance %q: %w", t.Name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(t, ep, resp.StatusCode, body)
	}
	// A JSON document cut at the cap is not a smaller answer but a corrupt
	// one, so an oversized body is refused rather than truncated.
	if int64(len(body)) > g.maxBytes {
		return nil, fmt.Errorf("%w: %s on instance %q returned more than %d bytes; narrow the request",
			ErrTooLarge, ep.name, t.Name, g.maxBytes)
	}
	return body, nil
}

// networkError turns the failure you will actually hit most often into the
// sentence that resolves it.
func networkError(t Target, err error) error {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf(
			"cannot reach Redash instance %q at %s. If this Redash is only reachable from the office network, check that your VPN is connected (underlying error: %v)",
			t.Name, t.BaseURL(), err)
	}
	return fmt.Errorf("request to Redash instance %q failed: %w", t.Name, err)
}

// ErrNotFound marks a 404, so a caller can explain what it means for its own
// endpoint: for a stored result, usually that the query has never been run.
var ErrNotFound = errors.New("not found")

func statusError(t Target, ep Endpoint, code int, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("Redash instance %q refused %s with %d: the API key may be wrong, or the service account may not have access to this object",
			t.Name, ep.name, code)
	case http.StatusNotFound:
		return fmt.Errorf("%w: Redash instance %q has no such object for %s (404)", ErrNotFound, t.Name, ep.name)
	case http.StatusTooManyRequests:
		return fmt.Errorf("Redash instance %q is rate limiting this client (429)", t.Name)
	}
	return fmt.Errorf("Redash instance %q returned %d for %s: %s", t.Name, code, ep.name, snippet(body))
}

// snippet bounds and de-fangs an error body. Redash error text is authored
// by whoever wrote the query, so it is untrusted: control characters that
// could be used to forge structure in the model's view are dropped.
func snippet(body []byte) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case r < 32 || r == 127:
			return -1
		}
		return r
	}, string(body))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
