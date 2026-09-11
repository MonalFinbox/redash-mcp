package policy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testKey = "s3cr3t-redash-key-do-not-log"

// recordingTransport is the tripwire. Any request that is not a GET fails
// the test at the transport layer, where no amount of tool-level refactoring
// can route around it.
type recordingTransport struct {
	t    *testing.T
	reqs []*http.Request
	body string
	code int
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.t.Helper()
	if r.Method != http.MethodGet {
		rt.t.Errorf("guard issued a %s request to %s; this program must only ever issue GET",
			r.Method, r.URL)
	}
	rt.reqs = append(rt.reqs, r)
	code := rt.code
	if code == 0 {
		code = http.StatusOK
	}
	body := rt.body
	if body == "" {
		body = `{"ok":true}`
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func testTarget(t *testing.T) (Target, *url.URL) {
	t.Helper()
	u, err := url.Parse("https://redash.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return NewTarget("prod", u, testKey, true), u
}

func newTestGuard(t *testing.T, tier Tier) (*Guard, *recordingTransport) {
	t.Helper()
	rt := &recordingTransport{t: t}
	return NewGuard(Options{Tier: tier, Transport: rt}), rt
}

// bindingFor supplies whatever the endpoint's path template needs.
func bindingFor(ep Endpoint) Binding {
	b := Binding{}
	if strings.Contains(ep.Path(), "{id}") {
		b.ID = 42
	}
	if strings.Contains(ep.Path(), "{slug}") {
		b.Slug = "weekly-disbursals"
	}
	return b
}

// TestOnlyGETIsEverIssued drives every reachable endpoint and asserts the
// transport never saw another method. This is the single assertion that
// would catch a regression a reviewer missed.
func TestOnlyGETIsEverIssued(t *testing.T) {
	g, rt := newTestGuard(t, TierExecute) // highest tier: widest surface
	tgt, _ := testTarget(t)

	issued := 0
	for _, ep := range All() {
		_, err := g.Get(context.Background(), tgt, ep, bindingFor(ep), nil)
		if ep.Method() == http.MethodGet {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", ep.Name(), err)
				continue
			}
			issued++
		} else if !errors.Is(err, ErrNotGet) {
			t.Errorf("%s: want ErrNotGet, got %v", ep.Name(), err)
		}
	}
	if issued == 0 {
		t.Fatal("no endpoint was exercised; the table is empty or bindingFor is wrong")
	}
	for _, r := range rt.reqs {
		if r.Method != http.MethodGet {
			t.Fatalf("recorded a %s request", r.Method)
		}
	}
}

func TestTierGateBlocksExecuteEndpointAtReadTier(t *testing.T) {
	g, rt := newTestGuard(t, TierRead)
	tgt, _ := testTarget(t)

	_, err := g.Get(context.Background(), tgt, RunSavedQuery, Binding{ID: 1}, nil)
	if !errors.Is(err, ErrTierDenied) {
		t.Fatalf("want ErrTierDenied at read tier, got %v", err)
	}
	if len(rt.reqs) != 0 {
		t.Fatalf("tier denial must happen before any request is sent, saw %d", len(rt.reqs))
	}
}

func TestUnregisteredEndpointRejected(t *testing.T) {
	g, rt := newTestGuard(t, TierExecute)
	tgt, _ := testTarget(t)

	// A zero-value Endpoint is the only thing another package could
	// construct, since every field is unexported.
	_, err := g.Get(context.Background(), tgt, Endpoint{}, Binding{}, nil)
	if !errors.Is(err, ErrUnregistered) {
		t.Fatalf("want ErrUnregistered, got %v", err)
	}

	// A forged endpoint that looks plausible must also be refused.
	forged := Endpoint{"delete_everything", http.MethodGet, "/api/queries/1", TierRead}
	if _, err := g.Get(context.Background(), tgt, forged, Binding{}, nil); !errors.Is(err, ErrUnregistered) {
		t.Fatalf("want ErrUnregistered for a forged endpoint, got %v", err)
	}
	if len(rt.reqs) != 0 {
		t.Fatalf("unregistered endpoints must not reach the transport, saw %d", len(rt.reqs))
	}
}

func TestPathBindingRejectsHostileValues(t *testing.T) {
	bad := []struct {
		name string
		ep   Endpoint
		b    Binding
	}{
		{"zero id", GetQuery, Binding{ID: 0}},
		{"negative id", GetQuery, Binding{ID: -1}},
		{"slug with traversal", GetDashboard, Binding{Slug: "../../api/users"}},
		{"slug with encoded traversal", GetDashboard, Binding{Slug: "%2e%2e%2fusers"}},
		{"slug with slash", GetDashboard, Binding{Slug: "a/b"}},
		{"slug with query smuggle", GetDashboard, Binding{Slug: "x?api_key=leak"}},
		{"slug with fragment", GetDashboard, Binding{Slug: "x#y"}},
		{"empty slug", GetDashboard, Binding{Slug: ""}},
		{"overlong slug", GetDashboard, Binding{Slug: strings.Repeat("a", 101)}},
	}

	g, rt := newTestGuard(t, TierExecute)
	tgt, _ := testTarget(t)

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := g.Get(context.Background(), tgt, tc.ep, tc.b, nil); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
	if len(rt.reqs) != 0 {
		t.Fatalf("no hostile binding may reach the transport, saw %d", len(rt.reqs))
	}
}

func TestAPIKeyTravelsInHeaderNeverInURL(t *testing.T) {
	g, rt := newTestGuard(t, TierRead)
	tgt, _ := testTarget(t)

	q := url.Values{"page_size": []string{"10"}}
	if _, err := g.Get(context.Background(), tgt, ListQueries, Binding{}, q); err != nil {
		t.Fatal(err)
	}
	if len(rt.reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(rt.reqs))
	}
	r := rt.reqs[0]

	if got := r.Header.Get("Authorization"); got != "Key "+testKey {
		t.Errorf("Authorization header = %q", got)
	}
	if strings.Contains(r.URL.String(), testKey) {
		t.Errorf("API key leaked into the URL: %s", r.URL)
	}
	if r.URL.Query().Get("page_size") != "10" {
		t.Errorf("query parameter lost: %s", r.URL)
	}
}

func TestErrorsNeverContainTheAPIKey(t *testing.T) {
	rt := &recordingTransport{t: t, code: http.StatusForbidden, body: `{"message":"nope"}`}
	g := NewGuard(Options{Tier: TierRead, Transport: rt})
	tgt, _ := testTarget(t)

	_, err := g.Get(context.Background(), tgt, ListQueries, Binding{}, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error text leaked the API key: %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var reached bool
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer secret.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, secret.URL, http.StatusFound)
	}))
	defer redirector.Close()

	u, err := url.Parse(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}
	g := NewGuard(Options{Tier: TierRead})
	tgt := NewTarget("uat", u, testKey, false)

	if _, err := g.Get(context.Background(), tgt, ListQueries, Binding{}, nil); err == nil {
		t.Fatal("want an error when Redash redirects")
	}
	if reached {
		t.Fatal("the redirect was followed; a redirect could aim the key at another host")
	}
}

// TestOversizedResponseIsRefused checks the cap refuses rather than cuts. A
// JSON document truncated at the cap is not a smaller answer but a corrupt
// one, and it used to surface as a baffling decode error.
func TestOversizedResponseIsRefused(t *testing.T) {
	rt := &recordingTransport{t: t, body: strings.Repeat("x", 4096)}
	g := NewGuard(Options{Tier: TierRead, Transport: rt, MaxBodyBytes: 512})
	tgt, _ := testTarget(t)

	if _, err := g.Get(context.Background(), tgt, ListQueries, Binding{}, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge for a body over the cap, got %v", err)
	}

	rt.body = strings.Repeat("x", 512)
	body, err := g.Get(context.Background(), tgt, ListQueries, Binding{}, nil)
	if err != nil {
		t.Fatalf("a body exactly at the cap should pass: %v", err)
	}
	if len(body) != 512 {
		t.Fatalf("got %d bytes, want 512", len(body))
	}
}

func TestSnippetStripsControlCharacters(t *testing.T) {
	in := []byte("line one\n\x00\x1b[31mstop\ttab")
	got := snippet(in)
	for _, r := range got {
		if r < 32 || r == 127 {
			t.Fatalf("control character %q survived in %q", r, got)
		}
	}
	if !strings.Contains(got, "stop") {
		t.Fatalf("legible text was destroyed: %q", got)
	}
}

func TestParseTier(t *testing.T) {
	for in, want := range map[string]Tier{"": TierRead, "read": TierRead, "execute": TierExecute} {
		got, err := ParseTier(in)
		if err != nil || got != want {
			t.Errorf("ParseTier(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseTier("admin"); err == nil {
		t.Error("ParseTier(\"admin\") should fail closed")
	}
}
