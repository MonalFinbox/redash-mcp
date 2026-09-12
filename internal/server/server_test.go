package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MonalFinbox/redash-mcp/internal/audit"
	"github.com/MonalFinbox/redash-mcp/internal/config"
	"github.com/MonalFinbox/redash-mcp/internal/policy"
	"github.com/MonalFinbox/redash-mcp/internal/redash"
)

// fakeRedash answers by path and fails the test on any non-GET request, so
// every test in this file doubles as a tripwire at the protocol boundary.
type fakeRedash struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string]string
	reqs   []*http.Request
}

func (f *fakeRedash) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		f.t.Errorf("a tool issued %s %s; only GET is permitted", r.Method, r.URL)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()

	body, ok := f.routes[r.URL.Path]
	code := http.StatusOK
	if !ok {
		code, body = http.StatusNotFound, `{"message": "No cached result found for this query."}`
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func (f *fakeRedash) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.reqs...)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	session *mcp.ClientSession
	fake    *fakeRedash
	audit   *syncBuffer
}

// start wires the real config, client, shaper, audit log and MCP server
// together, connected to an MCP client over an in-memory transport.
func start(t *testing.T, routes map[string]string, uatAllow []int, def string) *harness {
	t.Helper()
	fake := &fakeRedash{t: t, routes: routes}
	u, err := url.Parse("https://redash.invalid")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Order: []string{"prod", "uat"},
		Instances: map[string]*config.Instance{
			"prod": {Name: "prod", URL: u, APIKey: "prod-key", EnforcedReadOnly: true},
			"uat":  {Name: "uat", URL: u, APIKey: "uat-key", DataSources: uatAllow},
		},
		DefaultInstance: def,
		Tier:            policy.TierRead,
		MaxRows:         200,
		MaxBytes:        256 << 10,
		MaxCellChars:    512,
		Redact:          regexp.MustCompile(config.DefaultRedact),
		RateLimit:       1000,
		Timeout:         5 * time.Second,
	}

	instances := map[string]redash.Instance{}
	for name, target := range cfg.Targets() {
		instances[name] = redash.Instance{Target: target, DataSources: cfg.Instances[name].DataSources}
	}
	client := redash.New(redash.Options{
		Guard:           policy.NewGuard(policy.Options{Tier: cfg.Tier, Transport: fake}),
		Instances:       instances,
		DefaultInstance: def,
		RateLimit:       cfg.RateLimit,
	})
	buf := &syncBuffer{}
	srv := New(Options{Config: cfg, Client: client, Audit: audit.New(buf, nil), Version: "test"})

	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cs.Close()
		ss.Close()
	})
	return &harness{session: cs, fake: fake, audit: buf}
}

func (h *harness) call(t *testing.T, tool string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return res, text.String()
}

func storedResult(dataSourceID, rows int) string {
	var b strings.Builder
	for i := 0; i < rows; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"loan_id": %d, "customer_pan": "ABCDE%04dF", "status": "disbursed"}`, 1000+i, i)
	}
	return fmt.Sprintf(`{"query_result": {"id": 99, "data_source_id": %d, "retrieved_at": "2026-09-12T09:00:00+00:00",
		"runtime": 0.2, "query": "select * from loans", "data": {"columns": [
			{"name": "loan_id", "type": "integer"},
			{"name": "customer_pan", "type": "string"},
			{"name": "status", "type": "string"}
		], "rows": [%s]}}}`, dataSourceID, b.String())
}

func TestEveryToolIsReadOnlyAndCallable(t *testing.T) {
	h := start(t, map[string]string{
		"/api/data_sources":           `[{"id": 11, "name": "Warehouse", "type": "pg", "view_only": true}]`,
		"/api/data_sources/11/schema": `{"schema": [{"name": "loans", "columns": ["id"]}]}`,
		"/api/queries":                `{"count": 0, "page": 1, "page_size": 25, "results": []}`,
		"/api/queries/7":              `{"id": 7, "name": "Disbursals", "query": "select 1", "data_source_id": 11}`,
		"/api/queries/7/results":      storedResult(11, 2),
		"/api/dashboards":             `{"count": 0, "page": 1, "page_size": 25, "results": []}`,
		"/api/dashboards/ops":         `{"id": 1, "slug": "ops", "name": "Ops", "widgets": []}`,
	}, nil, "uat")

	listed, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != len(ToolNames()) {
		t.Fatalf("server lists %d tools, ToolNames has %d", len(listed.Tools), len(ToolNames()))
	}
	for _, tool := range listed.Tools {
		a := tool.Annotations
		if a == nil || !a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s is not annotated read-only and non-destructive: %+v", tool.Name, a)
		}
		if !strings.HasPrefix(tool.Name, "redash_") {
			t.Errorf("%s does not follow the redash_ naming convention", tool.Name)
		}
	}

	calls := map[string]map[string]any{
		toolGetSchema:    {"data_source_id": 11},
		toolListQueries:  {"search": "disbursal"},
		toolGetQuery:     {"query_id": 7},
		toolGetResults:   {"query_id": 7},
		toolGetDashboard: {"slug": "ops"},
	}
	for _, name := range ToolNames() {
		if res, text := h.call(t, name, calls[name]); res.IsError {
			t.Errorf("%s failed: %s", name, text)
		}
	}
}

func TestDefaultInstanceIsUsedOnlyWhenNoneIsNamed(t *testing.T) {
	h := start(t, map[string]string{"/api/data_sources": `[]`}, nil, "uat")

	h.call(t, toolListDataSources, nil)
	h.call(t, toolListDataSources, map[string]any{"instance": "prod"})

	reqs := h.fake.requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	if got := reqs[0].Header.Get("Authorization"); got != "Key uat-key" {
		t.Errorf("an unnamed instance should use the default uat key, got %q", got)
	}
	if got := reqs[1].Header.Get("Authorization"); got != "Key prod-key" {
		t.Errorf("a named instance should override the default, got %q", got)
	}
}

func TestWithoutADefaultInstanceIsRequired(t *testing.T) {
	h := start(t, map[string]string{"/api/data_sources": `[]`}, nil, "")

	res, text := h.call(t, toolListDataSources, nil)
	if !res.IsError || !strings.Contains(text, "instance") {
		t.Fatalf("a call naming no instance must fail when there is no default, got %q", text)
	}
	if n := len(h.fake.requests()); n != 0 {
		t.Fatalf("the refused call reached Redash %d times", n)
	}
}

func TestArgumentsOutsideTheSchemaNeverReachRedash(t *testing.T) {
	h := start(t, map[string]string{"/api/queries/7": `{"id": 7, "data_source_id": 11}`}, nil, "uat")

	cases := map[string]map[string]any{
		"undeclared sql argument": {"query_id": 7, "sql": "delete from loans"},
		"unknown instance":        {"query_id": 7, "instance": "staging"},
		"id as a path":            {"query_id": "7/../../users"},
		"zero id":                 {"query_id": 0},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			if res, text := h.call(t, toolGetQuery, a); !res.IsError {
				t.Fatalf("want a tool error, got %s", text)
			}
		})
	}
	if n := len(h.fake.requests()); n != 0 {
		t.Fatalf("invalid arguments reached Redash %d times", n)
	}
}

func TestResultsAreShapedRedactedAndAudited(t *testing.T) {
	h := start(t, map[string]string{"/api/queries/7/results": storedResult(11, 300)}, nil, "uat")

	res, text := h.call(t, toolGetResults, map[string]any{"query_id": 7})
	if res.IsError {
		t.Fatal(text)
	}
	var got struct {
		ReturnedRows    int      `json:"returned_rows"`
		TotalRows       int      `json:"total_rows"`
		RedactedColumns []string `json:"redacted_columns"`
		Truncated       []string `json:"truncated"`
		Notice          string   `json:"notice"`
		RetrievedAt     string   `json:"retrieved_at"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if got.ReturnedRows != 200 || got.TotalRows != 300 || len(got.Truncated) == 0 {
		t.Errorf("row cap not applied or not reported: %+v", got)
	}
	if strings.Join(got.RedactedColumns, ",") != "customer_pan" {
		t.Errorf("redacted columns = %v", got.RedactedColumns)
	}
	if strings.Contains(text, "ABCDE") {
		t.Error("a PAN value reached the tool output")
	}
	if strings.Contains(text, "select * from loans") {
		t.Error("the result echoed the query text")
	}
	if got.Notice == "" || got.RetrievedAt == "" {
		t.Errorf("provenance is missing: %+v", got)
	}

	log := h.audit.String()
	for _, want := range []string{`"tool":"redash_get_query_results"`, `"instance":"uat"`, `"outcome":"ok"`, `"rows":200`} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log is missing %s:\n%s", want, log)
		}
	}
	if strings.Contains(log, "ABCDE") {
		t.Error("result data reached the audit log")
	}
}

func TestResultsNeedExactlyOneID(t *testing.T) {
	h := start(t, nil, nil, "uat")
	for _, a := range []map[string]any{{}, {"query_id": 7, "result_id": 99}} {
		if res, text := h.call(t, toolGetResults, a); !res.IsError || !strings.Contains(text, "exactly one") {
			t.Errorf("args %v: want an exactly-one error, got %q", a, text)
		}
	}
	if n := len(h.fake.requests()); n != 0 {
		t.Fatalf("an ambiguous call reached Redash %d times", n)
	}
}

func TestMissingStoredResultExplainsThatNothingRuns(t *testing.T) {
	h := start(t, nil, nil, "uat")
	res, text := h.call(t, toolGetResults, map[string]any{"query_id": 8})
	if !res.IsError || !strings.Contains(text, "never runs queries") {
		t.Fatalf("want an explanation that nothing is executed, got %q", text)
	}
}

func TestWithheldDataSourceIsAToolErrorAndAudited(t *testing.T) {
	h := start(t, nil, []int{11}, "uat")

	res, text := h.call(t, toolGetSchema, map[string]any{"data_source_id": 19})
	if !res.IsError || !strings.Contains(text, "REDASH_UAT_DATA_SOURCES") {
		t.Fatalf("want a withheld error naming the setting, got %q", text)
	}
	if n := len(h.fake.requests()); n != 0 {
		t.Fatalf("a withheld schema was requested %d times", n)
	}
	if log := h.audit.String(); !strings.Contains(log, `"outcome":"error"`) || !strings.Contains(log, `"data_source_id":19`) {
		t.Errorf("the refusal was not audited:\n%s", log)
	}
}

func TestListInstancesReportsLayersWithoutTouchingRedash(t *testing.T) {
	h := start(t, nil, []int{11, 12, 13}, "uat")

	res, text := h.call(t, toolListInstances, nil)
	if res.IsError {
		t.Fatal(text)
	}
	var got instancesOut
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.DefaultInstance != "uat" || len(got.Instances) != 2 {
		t.Fatalf("unexpected instances: %+v", got)
	}
	for _, inst := range got.Instances {
		switch inst.Name {
		case "prod":
			if inst.ReadOnlyLayers != 2 {
				t.Errorf("prod has a View Only account and should report 2 layers, got %d", inst.ReadOnlyLayers)
			}
		case "uat":
			if inst.ReadOnlyLayers != 1 || len(inst.DataSourceAllowlist) != 3 || !inst.Default {
				t.Errorf("uat reported wrongly: %+v", inst)
			}
		}
	}
	if strings.Contains(text, "-key") {
		t.Error("an API key reached the tool output")
	}
	if n := len(h.fake.requests()); n != 0 {
		t.Fatalf("list_instances made %d requests; it must make none", n)
	}
}
