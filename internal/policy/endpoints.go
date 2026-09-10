package policy

import "net/http"

// Endpoint describes one request this program is permitted to make.
//
// Every field is unexported and the only instances are the package-level
// vars below, so no package outside policy can name a request this table
// does not already describe. Widening the program's reach means editing
// this file, which makes it visible in review as a security change rather
// than an incidental one.
type Endpoint struct {
	name   string
	method string
	path   string
	tier   Tier
}

func (e Endpoint) Name() string   { return e.name }
func (e Endpoint) Method() string { return e.method }
func (e Endpoint) Path() string   { return e.path }
func (e Endpoint) Tier() Tier     { return e.tier }

// The complete outbound surface.
var (
	ListQueries     = Endpoint{"list_queries", http.MethodGet, "/api/queries", TierRead}
	GetQuery        = Endpoint{"get_query", http.MethodGet, "/api/queries/{id}", TierRead}
	GetQueryResults = Endpoint{"get_query_results", http.MethodGet, "/api/queries/{id}/results", TierRead}
	GetResultByID   = Endpoint{"get_result_by_id", http.MethodGet, "/api/query_results/{id}.json", TierRead}
	ListDashboards  = Endpoint{"list_dashboards", http.MethodGet, "/api/dashboards", TierRead}
	GetDashboard    = Endpoint{"get_dashboard", http.MethodGet, "/api/dashboards/{slug}", TierRead}
	ListDataSources = Endpoint{"list_data_sources", http.MethodGet, "/api/data_sources", TierRead}
	GetSchema       = Endpoint{"get_schema", http.MethodGet, "/api/data_sources/{id}/schema", TierRead}

	// RunSavedQuery is declared so the ceiling lives in one table, but no
	// Guard method can issue a non-GET request. Until that changes, this
	// endpoint is unreachable by construction.
	RunSavedQuery = Endpoint{"run_saved_query", http.MethodPost, "/api/queries/{id}/results", TierExecute}
)

// all is a slice rather than a map so iteration order is deterministic in
// tests and in generated documentation.
var all = []Endpoint{
	ListQueries,
	GetQuery,
	GetQueryResults,
	GetResultByID,
	ListDashboards,
	GetDashboard,
	ListDataSources,
	GetSchema,
	RunSavedQuery,
}

// All returns a copy of the endpoint table.
func All() []Endpoint { return append([]Endpoint(nil), all...) }

func isRegistered(e Endpoint) bool {
	for _, x := range all {
		if x == e {
			return true
		}
	}
	return false
}
