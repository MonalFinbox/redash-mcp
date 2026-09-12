package shape

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MonalFinbox/redash-mcp/internal/redash"
)

var testRedact = regexp.MustCompile(`(?i)(^|_)(pan|email|phone)($|_)`)

func opts() Options {
	return Options{
		MaxRows:      200,
		MaxBytes:     256 << 10,
		MaxCellChars: 512,
		Redact:       testRedact,
		Now:          func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) },
	}
}

func sample(rows int) redash.Result {
	r := redash.Result{
		Instance:     "uat",
		QueryID:      7,
		ResultID:     99,
		DataSourceID: 11,
		RetrievedAt:  "2026-09-12T09:30:00.123456+00:00",
		Columns: []redash.Column{
			{Name: "loan_id", Type: "integer"},
			{Name: "company", Type: "string"},
			{Name: "customer_pan", Type: "string"},
			{Name: "contact", FriendlyName: "Primary Email", Type: "string"},
		},
	}
	for i := 0; i < rows; i++ {
		r.Rows = append(r.Rows, map[string]any{
			"loan_id":      json.Number(fmt.Sprint(1000 + i)),
			"company":      "Acme Lending",
			"customer_pan": "ABCDE1234F",
			"contact":      "a@b.invalid",
		})
	}
	return r
}

func TestRedactionByColumnAndFriendlyName(t *testing.T) {
	r := sample(2)
	r.Rows[1]["customer_pan"] = nil

	tbl, b, err := Result(r, opts())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ABCDE1234F") || strings.Contains(string(b), "a@b.invalid") {
		t.Fatalf("a redacted value reached the output: %s", b)
	}
	if got := strings.Join(tbl.RedactedColumns, ","); got != "customer_pan,contact" {
		t.Errorf("redacted columns = %q", got)
	}
	if tbl.Rows[0][1] != "Acme Lending" {
		t.Errorf("an ordinary column was masked: %v", tbl.Rows[0])
	}
	if tbl.Rows[1][2] != redacted {
		t.Errorf("a null in a masked column must still read as redacted, got %v", tbl.Rows[1][2])
	}
}

func TestUndeclaredRowKeysNeverReachTheOutput(t *testing.T) {
	r := sample(1)
	r.Rows[0]["aadhaar_undeclared"] = "1234-5678-9012"
	_, b, err := Result(r, opts())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "1234-5678-9012") {
		t.Fatalf("a key missing from the column list bypassed the column checks: %s", b)
	}
}

func TestRowCapIsReported(t *testing.T) {
	o := opts()
	o.MaxRows = 10
	tbl, _, err := Result(sample(25), o)
	if err != nil {
		t.Fatal(err)
	}
	if tbl.ReturnedRows != 10 || tbl.TotalRows != 25 || len(tbl.Rows) != 10 {
		t.Fatalf("returned %d of %d with %d rows", tbl.ReturnedRows, tbl.TotalRows, len(tbl.Rows))
	}
	if len(tbl.Truncated) != 1 || !strings.Contains(tbl.Truncated[0], "REDASH_MAX_ROWS") {
		t.Errorf("the row cut must be reported, got %v", tbl.Truncated)
	}
}

func TestCellCapIsRuneSafeAndCoversNestedValues(t *testing.T) {
	o := opts()
	o.MaxCellChars = 16
	r := sample(1)
	r.Rows[0]["company"] = strings.Repeat("ऋण", 20)
	r.Rows[0]["loan_id"] = map[string]any{"nested": strings.Repeat("x", 100)}

	tbl, _, err := Result(r, o)
	if err != nil {
		t.Fatal(err)
	}
	company := tbl.Rows[0][1].(string)
	if !strings.HasSuffix(company, "...") || len([]rune(strings.TrimSuffix(company, "..."))) != 16 {
		t.Errorf("multi-byte cell was not cut to 16 runes: %q", company)
	}
	if nested := tbl.Rows[0][0].(string); len(nested) > 19 {
		t.Errorf("nested value escaped the cell cap: %q", nested)
	}
	if len(tbl.Truncated) != 1 || !strings.Contains(tbl.Truncated[0], "2 values") {
		t.Errorf("cell cuts must be counted and reported, got %v", tbl.Truncated)
	}
}

func TestByteBudgetKeepsTheMostRowsThatFit(t *testing.T) {
	o := opts()
	o.MaxBytes = 4096
	tbl, b, err := Result(sample(500), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > o.MaxBytes {
		t.Fatalf("output is %d bytes, over the %d cap", len(b), o.MaxBytes)
	}
	if tbl.ReturnedRows == 0 || tbl.ReturnedRows >= 200 {
		t.Fatalf("expected a partial table, got %d rows", tbl.ReturnedRows)
	}
	if !strings.Contains(strings.Join(tbl.Truncated, " "), "REDASH_MAX_BYTES") {
		t.Errorf("the byte cut must be reported, got %v", tbl.Truncated)
	}

	// One more row would not have fitted.
	o.MaxRows = tbl.ReturnedRows + 1
	o.MaxBytes = 1 << 20
	bigger, _, err := Result(sample(500), o)
	if err != nil {
		t.Fatal(err)
	}
	bigger.Truncated = append(bigger.Truncated, tbl.Truncated[len(tbl.Truncated)-1])
	if more, _ := marshal(bigger); len(more) <= 4096 {
		t.Errorf("%d rows were returned but %d would also have fitted", tbl.ReturnedRows, tbl.ReturnedRows+1)
	}
}

func TestByteBudgetTooSmallForAnyRowsIsAnError(t *testing.T) {
	o := opts()
	o.MaxBytes = 64
	if _, _, err := Result(sample(3), o); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestAgeAndNoticeAreAlwaysPresent(t *testing.T) {
	tbl, _, err := Result(sample(1), opts())
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Age != "2h29m" {
		t.Errorf("age = %q, want 2h29m", tbl.Age)
	}
	if tbl.Notice != Notice {
		t.Error("the provenance notice is missing")
	}

	cases := map[string]string{
		"2026-09-12T11:59:30Z": "under a minute",
		"2026-09-12T11:15:00Z": "45m",
		"2026-09-02T12:00:00Z": "10d",
		"not a timestamp":      "",
	}
	for in, want := range cases {
		if got := age(in, opts().Now()); got != want {
			t.Errorf("age(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeRefusesOversizedOutputWithAHint(t *testing.T) {
	_, err := Encode(map[string]string{"x": strings.Repeat("y", 100)}, 50, "pass table_filter")
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "table_filter") {
		t.Fatalf("want ErrTooLarge carrying the hint, got %v", err)
	}
	b, err := Encode(map[string]string{"sql": "a < b"}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "a < b") {
		t.Errorf("HTML escaping mangled the output: %s", b)
	}
}

// TestRedactionSeesThroughSQLAliases is the regression test for a pattern
// anchored on underscores missing an alias like "Customer PAN", which is how
// Redash columns are usually named when a query renames them.
func TestRedactionSeesThroughSQLAliases(t *testing.T) {
	for _, name := range []string{"Customer PAN", "customer-pan", "customerPan", "PAN", "Primary Email", "phone#2"} {
		if !matches(testRedact, name) {
			t.Errorf("%q should be redacted", name)
		}
	}
	for _, name := range []string{"Company Name", "companyPanel", "Expanded View", "emailer template", ""} {
		if matches(testRedact, name) {
			t.Errorf("%q must not be redacted", name)
		}
	}
}
