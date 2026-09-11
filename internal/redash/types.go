package redash

// These are the only shapes this package hands onward. Redash responses are
// decoded into the wire structs in client.go, which name only the fields
// kept here, so everything else is dropped at the decoder rather than
// filtered out afterwards. That matters because Redash returns secrets
// alongside metadata: every query object carries its own api_key, the owner
// object carries an email address and group ids, and a parameter carries a
// default value that is often a real identifier.

type QueryPage struct {
	Instance string `json:"instance"`

	// Count is Redash's total for the search, before the data source
	// allowlist is applied.
	Count    int `json:"count"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`

	// HiddenByAllowlist is how many results on this page were dropped
	// because their data source is outside the instance's allowlist.
	HiddenByAllowlist int            `json:"hidden_by_allowlist,omitempty"`
	Queries           []QuerySummary `json:"queries"`
}

type QuerySummary struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	DataSourceID    int      `json:"data_source_id"`
	Tags            []string `json:"tags,omitempty"`
	Owner           string   `json:"owner,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
	HasCachedResult bool     `json:"has_cached_result"`
	RetrievedAt     string   `json:"retrieved_at,omitempty"`
	IsDraft         bool     `json:"is_draft,omitempty"`
	IsArchived      bool     `json:"is_archived,omitempty"`
}

type Query struct {
	Instance string `json:"instance"`
	QuerySummary
	SQL            string          `json:"sql"`
	Parameters     []Parameter     `json:"parameters,omitempty"`
	Visualizations []Visualization `json:"visualizations,omitempty"`
	LatestResultID int64           `json:"latest_result_id,omitempty"`
}

// Parameter describes a query parameter. Its default value is deliberately
// not decoded.
type Parameter struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	Type  string `json:"type"`
}

type Visualization struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Result is a stored query result, unshaped. Callers must pass it through
// the result shaper before handing it to a model.
type Result struct {
	Instance     string           `json:"instance"`
	ResultID     int64            `json:"result_id"`
	QueryID      int64            `json:"query_id,omitempty"`
	DataSourceID int              `json:"data_source_id"`
	RetrievedAt  string           `json:"retrieved_at"`
	Runtime      float64          `json:"runtime_seconds"`
	Columns      []Column         `json:"columns"`
	Rows         []map[string]any `json:"rows"`
}

type Column struct {
	Name         string `json:"name"`
	FriendlyName string `json:"friendly_name,omitempty"`
	Type         string `json:"type,omitempty"`
}

type DashboardPage struct {
	Instance   string             `json:"instance"`
	Count      int                `json:"count"`
	Page       int                `json:"page"`
	PageSize   int                `json:"page_size"`
	Dashboards []DashboardSummary `json:"dashboards"`
}

type DashboardSummary struct {
	ID         int64    `json:"id"`
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	Tags       []string `json:"tags,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
	IsDraft    bool     `json:"is_draft,omitempty"`
	IsArchived bool     `json:"is_archived,omitempty"`
}

type Dashboard struct {
	Instance string `json:"instance"`
	DashboardSummary
	Widgets []Widget `json:"widgets"`
}

type Widget struct {
	ID            int64                `json:"id"`
	Text          string               `json:"text,omitempty"`
	Visualization *WidgetVisualization `json:"visualization,omitempty"`

	// Withheld explains why a widget's visualization was left out.
	Withheld string `json:"withheld,omitempty"`
}

type WidgetVisualization struct {
	ID           int64  `json:"id"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	QueryID      int64  `json:"query_id"`
	QueryName    string `json:"query_name"`
	DataSourceID int    `json:"data_source_id"`
}

type DataSourceList struct {
	Instance          string       `json:"instance"`
	HiddenByAllowlist int          `json:"hidden_by_allowlist,omitempty"`
	DataSources       []DataSource `json:"data_sources"`
}

type DataSource struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`

	// ViewOnly is Redash's own statement that this key's account cannot
	// create or run queries against the data source.
	ViewOnly bool `json:"view_only"`
}

type Schema struct {
	Instance     string `json:"instance"`
	DataSourceID int    `json:"data_source_id"`

	// TableCount is the number of tables before any name filter.
	TableCount int     `json:"table_count"`
	Tables     []Table `json:"tables"`
}

type Table struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}
