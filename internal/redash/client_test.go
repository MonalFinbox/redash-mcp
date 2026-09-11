package redash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/MonalFinbox/redash-mcp/internal/policy"
)

// Values that must never appear in anything this package returns.
const (
	secretQueryKey = "QUERY-API-KEY-SECRET"
	secretEmail    = "asha@corp.invalid"
	secretParam    = "ABCDE1234F"
	secretSQL      = "select pan from borrowers"
)

// fakeRedash serves canned bodies by path, shaped like Redash 8 responses,
// including the fields Redash really sends that must be dropped.
type fakeRedash struct {
	t      *testing.T
	routes map[string]string
	reqs   []*http.Request
}

func (f *fakeRedash) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		f.t.Errorf("client issued %s %s; only GET is permitted", r.Method, r.URL)
	}
	f.reqs = append(f.reqs, r)
	body, ok := f.routes[r.URL.Path]
	code := http.StatusOK
	if !ok {
		code, body = http.StatusNotFound, `{"message":"not found"}`
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func query(id int64, dataSourceID int) string {
	return fmt.Sprintf(`{
		"id": %d, "name": "Query %d", "description": null, "query": %q,
		"data_source_id": %d, "api_key": %q, "tags": ["ops"],
		"user": {"id": 3, "name": "Asha", "email": %q, "groups": [1, 2]},
		"updated_at": "2026-09-01T10:00:00Z", "retrieved_at": "2026-09-01T09:00:00Z",
		"latest_query_data_id": 99, "is_draft": false, "is_archived": false,
		"options": {"parameters": [{"name": "pan", "title": "PAN", "type": "text", "value": %q, "global": false}]},
		"visualizations": [{"id": 5, "type": "TABLE", "name": "Table", "description": "", "options": {"x": 1}}]
	}`, id, id, secretSQL, dataSourceID, secretQueryKey, secretEmail, secretParam)
}

func result(dataSourceID int) string {
	return fmt.Sprintf(`{"query_result": {
		"id": 99, "query_hash": "h", "query": %q, "data_source_id": %d,
		"runtime": 0.5, "retrieved_at": "2026-09-01T09:00:00Z",
		"data": {"columns": [{"name": "loan_id", "friendly_name": "loan_id", "type": "integer"}],
		         "rows": [{"loan_id": 9007199254740993}]}
	}}`, secretSQL, dataSourceID)
}

func newTestClient(t *testing.T, routes map[string]string, allow []int, def string) (*Client, *fakeRedash) {
	t.Helper()
	fake := &fakeRedash{t: t, routes: routes}
	u, err := url.Parse("https://redash.invalid")
	if err != nil {
		t.Fatal(err)
	}
	g := policy.NewGuard(policy.Options{Tier: policy.TierRead, Transport: fake})
	c := New(Options{
		Guard: g,
		Instances: map[string]Instance{
			"uat":  {Target: policy.NewTarget("uat", u, "k", false), DataSources: allow},
			"prod": {Target: policy.NewTarget("prod", u, "k", true)},
		},
		DefaultInstance: def,
		RateLimit:       1000,
	})
	return c, fake
}

func assertNoSecrets(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{secretQueryKey, secretEmail, secretParam, "api_key", "email"} {
		if strings.Contains(string(b), s) {
			t.Errorf("output contains %q:\n%s", s, b)
		}
	}
}

func TestSecretFieldsAreDroppedAtTheDecoder(t *testing.T) {
	c, _ := newTestClient(t, map[string]string{
		"/api/queries":           `{"count": 1, "page": 1, "page_size": 25, "results": [` + query(7, 11) + `]}`,
		"/api/queries/7":         query(7, 11),
		"/api/queries/7/results": result(11),
	}, nil, "uat")
	ctx := context.Background()

	page, err := c.ListQueries(ctx, "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, page)

	q, err := c.GetQuery(ctx, "", 7)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, q)
	if q.Owner != "Asha" || len(q.Parameters) != 1 || q.Parameters[0].Name != "pan" {
		t.Errorf("safe fields were lost: %+v", q)
	}

	r, err := c.GetQueryResult(ctx, "", 7)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, r)
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), secretSQL) {
		t.Errorf("a result must not echo the query text: %s", b)
	}
}

func TestLargeIntegersKeepPrecision(t *testing.T) {
	c, _ := newTestClient(t, map[string]string{"/api/query_results/99.json": result(11)}, nil, "uat")
	r, err := c.GetResult(context.Background(), "", 99)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(r.Rows[0]["loan_id"]); got != "9007199254740993" {
		t.Fatalf("loan_id = %s; a float64 round trip would give 9007199254740992", got)
	}
}

func TestInstanceResolution(t *testing.T) {
	c, _ := newTestClient(t, nil, nil, "uat")
	if got, err := c.Resolve(""); err != nil || got != "uat" {
		t.Errorf("empty name should resolve to the default: %q, %v", got, err)
	}
	if got, err := c.Resolve("prod"); err != nil || got != "prod" {
		t.Errorf("an explicit name should win over the default: %q, %v", got, err)
	}
	_, err := c.Resolve("staging")
	if !errors.Is(err, ErrUnknownInstance) || !strings.Contains(err.Error(), "prod, uat") {
		t.Errorf("unknown instance should fail and list the valid names, got %v", err)
	}

	noDefault, _ := newTestClient(t, nil, nil, "")
	if _, err := noDefault.Resolve(""); !errors.Is(err, ErrNoInstance) {
		t.Errorf("with no default an empty name must fail, got %v", err)
	}
}

func TestAllowlistFiltersListings(t *testing.T) {
	c, _ := newTestClient(t, map[string]string{
		"/api/queries": `{"count": 3, "page": 1, "page_size": 25, "results": [` +
			query(7, 11) + `,` + query(8, 19) + `,` + query(9, 12) + `]}`,
		"/api/data_sources": `[{"id": 11, "name": "a", "type": "pg", "view_only": true},
		                       {"id": 19, "name": "b", "type": "pg", "view_only": true},
		                       {"id": 12, "name": "c", "type": "pg", "view_only": false}]`,
	}, []int{11, 12}, "uat")
	ctx := context.Background()

	page, err := c.ListQueries(ctx, "uat", "", 1, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Queries) != 2 || page.HiddenByAllowlist != 1 {
		t.Fatalf("want 2 visible and 1 hidden, got %d and %d", len(page.Queries), page.HiddenByAllowlist)
	}
	for _, q := range page.Queries {
		if q.DataSourceID == 19 {
			t.Error("a query on a withheld data source was listed")
		}
	}

	ds, err := c.ListDataSources(ctx, "uat")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.DataSources) != 2 || ds.HiddenByAllowlist != 1 {
		t.Fatalf("want 2 visible and 1 hidden data sources, got %+v", ds)
	}

	// prod has no allowlist, so the same response is shown in full.
	full, err := c.ListDataSources(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(full.DataSources) != 3 {
		t.Fatalf("an instance without an allowlist should see everything, got %d", len(full.DataSources))
	}
}

func TestAllowlistBlocksObjectsOnWithheldSources(t *testing.T) {
	c, _ := newTestClient(t, map[string]string{
		"/api/queries/8":             query(8, 19),
		"/api/queries/8/results":     result(19),
		"/api/query_results/99.json": result(19),
	}, []int{11}, "uat")
	ctx := context.Background()

	if _, err := c.GetQuery(ctx, "", 8); !errors.Is(err, ErrDataSourceWithheld) {
		t.Errorf("GetQuery: want ErrDataSourceWithheld, got %v", err)
	}
	if _, err := c.GetQueryResult(ctx, "", 8); !errors.Is(err, ErrDataSourceWithheld) {
		t.Errorf("GetQueryResult: want ErrDataSourceWithheld, got %v", err)
	}
	_, err := c.GetResult(ctx, "", 99)
	if !errors.Is(err, ErrDataSourceWithheld) {
		t.Errorf("GetResult: want ErrDataSourceWithheld, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "REDASH_UAT_DATA_SOURCES") {
		t.Errorf("the error should name the setting that fixes it, got %v", err)
	}
}

func TestSchemaOnWithheldSourceIsRefusedBeforeAnyRequest(t *testing.T) {
	c, fake := newTestClient(t, nil, []int{11}, "uat")
	if _, err := c.GetSchema(context.Background(), "", 19, ""); !errors.Is(err, ErrDataSourceWithheld) {
		t.Fatalf("want ErrDataSourceWithheld, got %v", err)
	}
	if len(fake.reqs) != 0 {
		t.Fatalf("a withheld data source must not be requested at all, saw %d requests", len(fake.reqs))
	}
}

func TestSchemaFormatsAndFilter(t *testing.T) {
	c, _ := newTestClient(t, map[string]string{
		"/api/data_sources/11/schema": `{"schema": [
			{"name": "loans", "columns": ["id", "amount"]},
			{"name": "collections", "columns": [{"name": "id", "type": "int"}, {"name": "score", "type": "int"}]},
			{"name": "loan_events", "columns": []}
		]}`,
		"/api/data_sources/12/schema": `{"error": {"code": 1, "message": "boom\u001b[31m"}}`,
		"/api/data_sources/13/schema": `{"job": {"id": "abc", "status": 1}}`,
	}, []int{11, 12, 13}, "uat")
	ctx := context.Background()

	s, err := c.GetSchema(ctx, "", 11, "")
	if err != nil {
		t.Fatal(err)
	}
	if s.TableCount != 3 || len(s.Tables) != 3 || s.Tables[0].Name != "collections" {
		t.Fatalf("unexpected schema: %+v", s)
	}
	if got := strings.Join(s.Tables[0].Columns, ","); got != "id,score" {
		t.Errorf("object-form columns = %q", got)
	}

	filtered, err := c.GetSchema(ctx, "", 11, "LOAN")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Tables) != 2 || filtered.TableCount != 3 {
		t.Errorf("filter should be a case-insensitive substring, got %+v", filtered)
	}

	if _, err := c.GetSchema(ctx, "", 12, ""); err == nil || strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("a schema error should surface with control characters removed, got %v", err)
	}
	if _, err := c.GetSchema(ctx, "", 13, ""); err == nil || !strings.Contains(err.Error(), "try again") {
		t.Errorf("a pending refresh should say to try again, got %v", err)
	}
}

func TestDashboardWithholdsWidgetsOnDisallowedSources(t *testing.T) {
	c, fake := newTestClient(t, map[string]string{
		"/api/dashboards/ops": fmt.Sprintf(`{"id": 1, "slug": "ops", "name": "Ops", "tags": [], "api_key": %q,
			"user": {"email": %q},
			"widgets": [
				{"id": 10, "text": "", "visualization": {"id": 5, "type": "TABLE", "name": "T",
				  "query": {"id": 7, "name": "Disbursals", "data_source_id": 11, "api_key": %q}}},
				{"id": 11, "text": "", "visualization": {"id": 6, "type": "CHART", "name": "C",
				  "query": {"id": 8, "name": "Collections", "data_source_id": 19, "api_key": %q}}},
				{"id": 12, "text": "## notes", "visualization": null}
			]}`, secretQueryKey, secretEmail, secretQueryKey, secretQueryKey),
	}, []int{11}, "uat")

	d, err := c.GetDashboard(context.Background(), "", "ops")
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, d)
	if len(d.Widgets) != 3 {
		t.Fatalf("widgets should keep their positions, got %d", len(d.Widgets))
	}
	if d.Widgets[0].Visualization == nil || d.Widgets[0].Visualization.QueryName != "Disbursals" {
		t.Errorf("an allowed widget was lost: %+v", d.Widgets[0])
	}
	if d.Widgets[1].Visualization != nil || d.Widgets[1].Withheld == "" {
		t.Errorf("a widget on a withheld source must be emptied and marked: %+v", d.Widgets[1])
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "Collections") {
		t.Errorf("a withheld widget leaked its query name: %s", b)
	}
	if d.Widgets[2].Text != "## notes" {
		t.Errorf("a text widget was lost: %+v", d.Widgets[2])
	}

	if _, ok := fake.reqs[0].URL.Query()["legacy"]; !ok {
		t.Errorf("dashboard requests must send legacy so newer Redash resolves the slug: %s", fake.reqs[0].URL)
	}
}

func TestPagingIsClampedAndSearchBounded(t *testing.T) {
	c, fake := newTestClient(t, map[string]string{
		"/api/queries":    `{"count": 0, "page": 1, "page_size": 100, "results": []}`,
		"/api/dashboards": `{"count": 0, "page": 1, "page_size": 25, "results": []}`,
	}, nil, "uat")
	ctx := context.Background()

	if _, err := c.ListQueries(ctx, "", "  loans ", -3, 5000); err != nil {
		t.Fatal(err)
	}
	q := fake.reqs[0].URL.Query()
	if q.Get("page") != "1" || q.Get("page_size") != "100" || q.Get("q") != "loans" {
		t.Errorf("paging was not clamped: %s", fake.reqs[0].URL.RawQuery)
	}

	if _, err := c.ListDashboards(ctx, "", strings.Repeat("x", 201), 1, 0); !errors.Is(err, ErrBadArgument) {
		t.Errorf("overlong search should be refused, got %v", err)
	}
	if len(fake.reqs) != 1 {
		t.Errorf("a refused argument must not reach Redash, saw %d requests", len(fake.reqs))
	}
}

func TestRateLimitIsAppliedBeforeTheRequest(t *testing.T) {
	c, fake := newTestClient(t, map[string]string{"/api/data_sources": `[]`}, nil, "uat")
	c.limit = newLimiter(1, nil)
	ctx := context.Background()

	if _, err := c.ListDataSources(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListDataSources(ctx, ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	if len(fake.reqs) != 1 {
		t.Fatalf("a rate-limited call must not reach Redash, saw %d requests", len(fake.reqs))
	}
}
