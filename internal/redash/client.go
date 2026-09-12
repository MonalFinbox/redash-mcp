// Package redash is a typed, read-only view of the Redash API.
//
// It holds no HTTP client and builds no URLs: every call names an endpoint
// from the policy table and goes through policy.Guard. What it adds is what
// the guard cannot know: which instance a caller meant, which data sources
// that instance may expose, how often it may be asked, and which fields of
// each response are safe to hand onward.
package redash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MonalFinbox/redash-mcp/internal/policy"
)

var (
	ErrUnknownInstance    = errors.New("unknown Redash instance")
	ErrNoInstance         = errors.New("no instance given and no default instance is configured")
	ErrDataSourceWithheld = errors.New("data source is not in this instance's allowlist")
	ErrRateLimited        = errors.New("rate limit reached")
	ErrBadArgument        = errors.New("invalid argument")
)

const (
	DefaultPageSize = 25
	MaxPageSize     = 100
	maxSearchChars  = 200
)

// Instance is one Redash deployment as the client sees it.
type Instance struct {
	Target policy.Target

	// DataSources restricts what the instance may expose. Empty means every
	// data source the key can see.
	DataSources []int
}

type Options struct {
	Guard           *policy.Guard
	Instances       map[string]Instance
	DefaultInstance string

	// RateLimit is requests per minute across all instances.
	RateLimit int

	// Now is a test seam for the rate limiter.
	Now func() time.Time
}

type Client struct {
	guard   *policy.Guard
	targets map[string]policy.Target
	allow   map[string]map[int]bool
	names   []string
	def     string
	limit   *limiter
}

func New(o Options) *Client {
	c := &Client{
		guard:   o.Guard,
		targets: map[string]policy.Target{},
		allow:   map[string]map[int]bool{},
		def:     o.DefaultInstance,
		limit:   newLimiter(o.RateLimit, o.Now),
	}
	for name, inst := range o.Instances {
		c.targets[name] = inst.Target
		c.names = append(c.names, name)
		if len(inst.DataSources) > 0 {
			set := map[int]bool{}
			for _, id := range inst.DataSources {
				set[id] = true
			}
			c.allow[name] = set
		}
	}
	sort.Strings(c.names)
	return c
}

// Instances returns the configured instance names, sorted.
func (c *Client) Instances() []string { return slices.Clone(c.names) }

func (c *Client) DefaultInstance() string { return c.def }

// Resolve maps the instance a caller named, possibly none, to a configured
// one. The error lists the valid names so a model can correct itself.
func (c *Client) Resolve(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if c.def == "" {
			return "", fmt.Errorf("%w; pass instance as one of: %s", ErrNoInstance, strings.Join(c.names, ", "))
		}
		return c.def, nil
	}
	if _, ok := c.targets[name]; !ok {
		return "", fmt.Errorf("%w %q; configured instances are: %s", ErrUnknownInstance, name, strings.Join(c.names, ", "))
	}
	return name, nil
}

// Allowed reports whether an instance may expose a data source.
func (c *Client) Allowed(instance string, dataSourceID int) bool {
	set, restricted := c.allow[instance]
	return !restricted || set[dataSourceID]
}

func (c *Client) ListQueries(ctx context.Context, instance, search string, page, pageSize int) (QueryPage, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return QueryPage{}, err
	}
	q, err := pageQuery(search, page, pageSize)
	if err != nil {
		return QueryPage{}, err
	}

	var w struct {
		Count    int         `json:"count"`
		Page     int         `json:"page"`
		PageSize int         `json:"page_size"`
		Results  []wireQuery `json:"results"`
	}
	if err := c.get(ctx, inst, policy.ListQueries, policy.Binding{}, q, &w); err != nil {
		return QueryPage{}, err
	}

	out := QueryPage{Instance: inst, Count: w.Count, Page: w.Page, PageSize: w.PageSize, Queries: []QuerySummary{}}
	for _, r := range w.Results {
		if !c.Allowed(inst, r.DataSourceID) {
			out.HiddenByAllowlist++
			continue
		}
		out.Queries = append(out.Queries, r.summary())
	}
	return out, nil
}

func (c *Client) GetQuery(ctx context.Context, instance string, id int64) (Query, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return Query{}, err
	}
	var w wireQuery
	if err := c.get(ctx, inst, policy.GetQuery, policy.Binding{ID: id}, nil, &w); err != nil {
		return Query{}, err
	}
	if !c.Allowed(inst, w.DataSourceID) {
		return Query{}, withheld(inst, "query", id, w.DataSourceID)
	}
	return Query{
		Instance:       inst,
		QuerySummary:   w.summary(),
		SQL:            w.Query,
		Parameters:     w.Options.Parameters,
		Visualizations: w.Visualizations,
		LatestResultID: w.LatestQueryDataID,
	}, nil
}

// GetQueryResult returns the result Redash has stored for a saved query. It
// never causes the query to run.
//
// The allowlist is checked against the result's own data source, after the
// response arrives. Checking the query first would cost a second request
// for no gain: nothing leaves this process until the check has passed.
func (c *Client) GetQueryResult(ctx context.Context, instance string, queryID int64) (Result, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return Result{}, err
	}
	r, err := c.result(ctx, inst, policy.GetQueryResults, queryID)
	if err != nil {
		return Result{}, err
	}
	r.QueryID = queryID
	return r, nil
}

// GetResult returns a stored result by its own id.
func (c *Client) GetResult(ctx context.Context, instance string, resultID int64) (Result, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return Result{}, err
	}
	return c.result(ctx, inst, policy.GetResultByID, resultID)
}

func (c *Client) result(ctx context.Context, inst string, ep policy.Endpoint, id int64) (Result, error) {
	var w struct {
		QueryResult struct {
			ID           int64   `json:"id"`
			DataSourceID int     `json:"data_source_id"`
			RetrievedAt  string  `json:"retrieved_at"`
			Runtime      float64 `json:"runtime"`
			Data         struct {
				Columns []Column         `json:"columns"`
				Rows    []map[string]any `json:"rows"`
			} `json:"data"`
		} `json:"query_result"`
	}
	if err := c.get(ctx, inst, ep, policy.Binding{ID: id}, nil, &w); err != nil {
		return Result{}, err
	}
	qr := w.QueryResult
	if !c.Allowed(inst, qr.DataSourceID) {
		return Result{}, withheld(inst, "result", id, qr.DataSourceID)
	}
	return Result{
		Instance:     inst,
		ResultID:     qr.ID,
		DataSourceID: qr.DataSourceID,
		RetrievedAt:  qr.RetrievedAt,
		Runtime:      qr.Runtime,
		Columns:      qr.Data.Columns,
		Rows:         qr.Data.Rows,
	}, nil
}

func (c *Client) ListDashboards(ctx context.Context, instance, search string, page, pageSize int) (DashboardPage, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return DashboardPage{}, err
	}
	q, err := pageQuery(search, page, pageSize)
	if err != nil {
		return DashboardPage{}, err
	}

	var w struct {
		Count    int             `json:"count"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
		Results  []wireDashboard `json:"results"`
	}
	if err := c.get(ctx, inst, policy.ListDashboards, policy.Binding{}, q, &w); err != nil {
		return DashboardPage{}, err
	}
	out := DashboardPage{Instance: inst, Count: w.Count, Page: w.Page, PageSize: w.PageSize, Dashboards: []DashboardSummary{}}
	for _, d := range w.Results {
		out.Dashboards = append(out.Dashboards, d.summary())
	}
	return out, nil
}

// GetDashboard fetches a dashboard by slug. Widgets whose query uses a data
// source outside the allowlist keep their position but lose their content.
//
// Redash 8 addresses dashboards by slug and later versions by id, resolving
// a slug only when "legacy" is present. Sending it always works on both.
func (c *Client) GetDashboard(ctx context.Context, instance, slug string) (Dashboard, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return Dashboard{}, err
	}
	var w wireDashboard
	q := url.Values{"legacy": {""}}
	if err := c.get(ctx, inst, policy.GetDashboard, policy.Binding{Slug: slug}, q, &w); err != nil {
		return Dashboard{}, err
	}

	out := Dashboard{Instance: inst, DashboardSummary: w.summary(), Widgets: []Widget{}}
	for _, wd := range w.Widgets {
		widget := Widget{ID: wd.ID, Text: wd.Text}
		if v := wd.Visualization; v != nil {
			if c.Allowed(inst, v.Query.DataSourceID) {
				widget.Visualization = &WidgetVisualization{
					ID:           v.ID,
					Type:         v.Type,
					Name:         v.Name,
					QueryID:      v.Query.ID,
					QueryName:    v.Query.Name,
					DataSourceID: v.Query.DataSourceID,
				}
			} else {
				widget.Withheld = "visualization uses a data source outside this instance's allowlist"
			}
		}
		out.Widgets = append(out.Widgets, widget)
	}
	return out, nil
}

func (c *Client) ListDataSources(ctx context.Context, instance string) (DataSourceList, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return DataSourceList{}, err
	}
	var w []DataSource
	if err := c.get(ctx, inst, policy.ListDataSources, policy.Binding{}, nil, &w); err != nil {
		return DataSourceList{}, err
	}
	out := DataSourceList{Instance: inst, DataSources: []DataSource{}}
	for _, ds := range w {
		if !c.Allowed(inst, ds.ID) {
			out.HiddenByAllowlist++
			continue
		}
		out.DataSources = append(out.DataSources, ds)
	}
	sort.Slice(out.DataSources, func(i, j int) bool { return out.DataSources[i].ID < out.DataSources[j].ID })
	return out, nil
}

// GetSchema lists a data source's tables, optionally filtered by a
// case-insensitive substring of the table name. The allowlist is checked
// before any request is made.
func (c *Client) GetSchema(ctx context.Context, instance string, dataSourceID int, tableFilter string) (Schema, error) {
	inst, err := c.Resolve(instance)
	if err != nil {
		return Schema{}, err
	}
	if !c.Allowed(inst, dataSourceID) {
		return Schema{}, withheld(inst, "data source", int64(dataSourceID), dataSourceID)
	}

	var w struct {
		Schema []struct {
			Name    string            `json:"name"`
			Columns []json.RawMessage `json:"columns"`
		} `json:"schema"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Job json.RawMessage `json:"job"`
	}
	if err := c.get(ctx, inst, policy.GetSchema, policy.Binding{ID: int64(dataSourceID)}, nil, &w); err != nil {
		return Schema{}, err
	}
	if w.Error != nil {
		return Schema{}, fmt.Errorf("instance %q could not load the schema for data source %d: %s", inst, dataSourceID, clean(w.Error.Message))
	}
	if len(w.Schema) == 0 && len(w.Job) > 0 {
		return Schema{}, fmt.Errorf("instance %q is still refreshing the schema for data source %d; try again shortly", inst, dataSourceID)
	}

	filter := strings.ToLower(strings.TrimSpace(tableFilter))
	out := Schema{Instance: inst, DataSourceID: dataSourceID, TableCount: len(w.Schema), Tables: []Table{}}
	for _, t := range w.Schema {
		if filter != "" && !strings.Contains(strings.ToLower(t.Name), filter) {
			continue
		}
		cols := make([]string, 0, len(t.Columns))
		for _, raw := range t.Columns {
			if name := columnName(raw); name != "" {
				cols = append(cols, name)
			}
		}
		out.Tables = append(out.Tables, Table{Name: t.Name, Columns: cols})
	}
	sort.Slice(out.Tables, func(i, j int) bool { return out.Tables[i].Name < out.Tables[j].Name })
	return out, nil
}

func (c *Client) get(ctx context.Context, inst string, ep policy.Endpoint, b policy.Binding, q url.Values, out any) error {
	if ok, wait := c.limit.take(); !ok {
		return fmt.Errorf("%w: retry in %s", ErrRateLimited, (wait + time.Second - 1).Truncate(time.Second))
	}
	body, err := c.guard.Get(ctx, c.targets[inst], ep, b, q)
	if err != nil {
		return err
	}
	// UseNumber keeps large integer ids exact instead of rounding them
	// through float64.
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("instance %q answered %s with unexpected JSON: %w", inst, ep.Name(), err)
	}
	return nil
}

func pageQuery(search string, page, pageSize int) (url.Values, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	pageSize = min(pageSize, MaxPageSize)

	q := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(pageSize)}}
	if s := strings.TrimSpace(search); s != "" {
		if len([]rune(s)) > maxSearchChars {
			return nil, fmt.Errorf("%w: search text is limited to %d characters", ErrBadArgument, maxSearchChars)
		}
		q.Set("q", s)
	}
	return q, nil
}

func withheld(inst, kind string, id int64, dataSourceID int) error {
	fix := fmt.Sprintf("To allow it, add %d to REDASH_%s_DATA_SOURCES", dataSourceID, strings.ToUpper(inst))
	if kind == "data source" {
		return fmt.Errorf("%w: data source %d on instance %q. %s", ErrDataSourceWithheld, dataSourceID, inst, fix)
	}
	return fmt.Errorf("%w: %s %d on instance %q uses data source %d. %s",
		ErrDataSourceWithheld, kind, id, inst, dataSourceID, fix)
}

// columnName accepts both schema column formats: Redash 8 sends bare names,
// later versions send objects with a name and a type.
func columnName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Name
	}
	return ""
}

// clean bounds untrusted text from Redash, such as an error message, and
// drops control characters that could forge structure in a model's view.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "..."
	}
	return s
}

type wireUser struct {
	Name string `json:"name"`
}

type wireQuery struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	Query             string    `json:"query"`
	DataSourceID      int       `json:"data_source_id"`
	Tags              []string  `json:"tags"`
	User              *wireUser `json:"user"`
	UpdatedAt         string    `json:"updated_at"`
	RetrievedAt       string    `json:"retrieved_at"`
	LatestQueryDataID int64     `json:"latest_query_data_id"`
	IsDraft           bool      `json:"is_draft"`
	IsArchived        bool      `json:"is_archived"`
	Options           struct {
		Parameters []Parameter `json:"parameters"`
	} `json:"options"`
	Visualizations []Visualization `json:"visualizations"`
}

func (w wireQuery) summary() QuerySummary {
	s := QuerySummary{
		ID:              w.ID,
		Name:            w.Name,
		Description:     w.Description,
		DataSourceID:    w.DataSourceID,
		Tags:            w.Tags,
		UpdatedAt:       w.UpdatedAt,
		HasCachedResult: w.LatestQueryDataID > 0,
		RetrievedAt:     w.RetrievedAt,
		IsDraft:         w.IsDraft,
		IsArchived:      w.IsArchived,
	}
	if w.User != nil {
		s.Owner = w.User.Name
	}
	return s
}

type wireDashboard struct {
	ID         int64    `json:"id"`
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	Tags       []string `json:"tags"`
	UpdatedAt  string   `json:"updated_at"`
	IsDraft    bool     `json:"is_draft"`
	IsArchived bool     `json:"is_archived"`
	Widgets    []struct {
		ID            int64  `json:"id"`
		Text          string `json:"text"`
		Visualization *struct {
			ID    int64  `json:"id"`
			Type  string `json:"type"`
			Name  string `json:"name"`
			Query struct {
				ID           int64  `json:"id"`
				Name         string `json:"name"`
				DataSourceID int    `json:"data_source_id"`
			} `json:"query"`
		} `json:"visualization"`
	} `json:"widgets"`
}

func (w wireDashboard) summary() DashboardSummary {
	return DashboardSummary{
		ID:         w.ID,
		Slug:       w.Slug,
		Name:       w.Name,
		Tags:       w.Tags,
		UpdatedAt:  w.UpdatedAt,
		IsDraft:    w.IsDraft,
		IsArchived: w.IsArchived,
	}
}
