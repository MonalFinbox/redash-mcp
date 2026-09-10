package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MonalFinbox/redash-mcp/internal/policy"
)

// envMap builds an Env from a literal map so tests never read os.Environ.
func envMap(m map[string]string) Env {
	return func(k string) string { return m[k] }
}

// keyFile writes a key with the given permissions and returns its path.
func keyFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "redash.key")
	if err := os.WriteFile(p, []byte("test-key-value\n"), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile is subject to umask, so set the mode explicitly.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// baseEnv is a minimal valid configuration. redash.invalid is in a reserved
// TLD, so it never resolves and the test never depends on the network.
func baseEnv(t *testing.T) map[string]string {
	return map[string]string{
		"REDASH_INSTANCES":              "prod",
		"REDASH_PROD_URL":               "https://redash.invalid",
		"REDASH_PROD_API_KEY_FILE":      keyFile(t, 0o600),
		"REDASH_PROD_ENFORCED_READONLY": "true",
	}
}

func TestLoadMinimalConfig(t *testing.T) {
	c, err := Load(envMap(baseEnv(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Tier != policy.TierRead {
		t.Errorf("default tier = %v, want read", c.Tier)
	}
	inst, ok := c.Instances["prod"]
	if !ok {
		t.Fatal("prod instance missing")
	}
	if inst.APIKey != "test-key-value" {
		t.Errorf("key = %q (trailing newline should be trimmed)", inst.APIKey)
	}
	if !inst.EnforcedReadOnly {
		t.Error("EnforcedReadOnly should be true")
	}
	if c.MaxRows != DefaultMaxRows {
		t.Errorf("MaxRows = %d", c.MaxRows)
	}
}

func TestMissingInstancesIsAnError(t *testing.T) {
	if _, err := Load(envMap(map[string]string{})); err == nil {
		t.Fatal("want an error when REDASH_INSTANCES is unset")
	}
}

func TestKeyFileMustNotBeGroupOrWorldReadable(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o666} {
		e := baseEnv(t)
		e["REDASH_PROD_API_KEY_FILE"] = keyFile(t, mode)
		_, err := Load(envMap(e))
		if err == nil {
			t.Errorf("mode %04o: want an error, got nil", mode)
			continue
		}
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %04o: error should tell the user how to fix it, got: %v", mode, err)
		}
	}
}

func TestInlineKeyIsAcceptedButFileWins(t *testing.T) {
	e := baseEnv(t)
	e["REDASH_PROD_API_KEY"] = "inline-key"
	c, err := Load(envMap(e))
	if err != nil {
		t.Fatal(err)
	}
	if c.Instances["prod"].APIKey != "test-key-value" {
		t.Error("the key file should take precedence over the inline key")
	}

	delete(e, "REDASH_PROD_API_KEY_FILE")
	c, err = Load(envMap(e))
	if err != nil {
		t.Fatal(err)
	}
	if c.Instances["prod"].APIKey != "inline-key" {
		t.Error("the inline key should be used when no file is configured")
	}
}

func TestNoKeyAtAllIsAnError(t *testing.T) {
	e := baseEnv(t)
	delete(e, "REDASH_PROD_API_KEY_FILE")
	if _, err := Load(envMap(e)); err == nil {
		t.Fatal("want an error when no key is configured")
	}
}

func TestURLRules(t *testing.T) {
	cases := map[string]struct {
		url     string
		wantErr bool
	}{
		"https is fine":           {"https://redash.invalid", false},
		"http is refused":         {"http://redash.invalid", true},
		"ftp is refused":          {"ftp://redash.invalid", true},
		"no scheme is refused":    {"redash.invalid", true},
		"query string is refused": {"https://redash.invalid?api_key=leak", true},
		"fragment is refused":     {"https://redash.invalid#x", true},
		"embedded creds refused":  {"https://user:pw@redash.invalid", true},
		"loopback https refused":  {"https://127.0.0.1", true}, // private, not opted in
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := baseEnv(t)
			e["REDASH_PROD_URL"] = tc.url
			_, err := Load(envMap(e))
			if tc.wantErr && err == nil {
				t.Fatalf("%q: want an error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("%q: unexpected error: %v", tc.url, err)
			}
		})
	}
}

func TestPrivateAddressNeedsOptIn(t *testing.T) {
	e := baseEnv(t)
	e["REDASH_PROD_URL"] = "https://127.0.0.1"

	_, err := Load(envMap(e))
	if err == nil {
		t.Fatal("a private address must be refused by default")
	}
	if !strings.Contains(err.Error(), "REDASH_ALLOW_PRIVATE_ADDRS") {
		t.Errorf("the error should name the opt-in flag, got: %v", err)
	}

	e["REDASH_ALLOW_PRIVATE_ADDRS"] = "true"
	if _, err := Load(envMap(e)); err != nil {
		t.Fatalf("with the opt-in set it should load: %v", err)
	}
}

func TestUnresolvableHostIsAWarningNotAnError(t *testing.T) {
	c, err := Load(envMap(baseEnv(t)))
	if err != nil {
		t.Fatalf("a host that does not resolve must not stop startup: %v", err)
	}
	var found bool
	for _, w := range c.Warnings {
		if strings.Contains(w, "does not resolve") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning about the unresolvable host, got %v", c.Warnings)
	}
}

func TestUnenforcedInstanceWarns(t *testing.T) {
	e := baseEnv(t)
	e["REDASH_PROD_ENFORCED_READONLY"] = "false"
	c, err := Load(envMap(e))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range c.Warnings {
		if strings.Contains(w, "only thing preventing a write") {
			found = true
		}
	}
	if !found {
		t.Errorf("an unenforced instance should warn, got %v", c.Warnings)
	}
}

func TestDuplicateInstanceIsAnError(t *testing.T) {
	e := baseEnv(t)
	e["REDASH_INSTANCES"] = "prod,prod"
	if _, err := Load(envMap(e)); err == nil {
		t.Fatal("want an error for a duplicated instance name")
	}
}

func TestDataSourceAllowlist(t *testing.T) {
	e := baseEnv(t)
	e["REDASH_PROD_DATA_SOURCES"] = "3, 14, 22"
	c, err := Load(envMap(e))
	if err != nil {
		t.Fatal(err)
	}
	got := c.Instances["prod"].DataSources
	want := []int{3, 14, 22}
	if len(got) != len(want) {
		t.Fatalf("data sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("data sources = %v, want %v", got, want)
		}
	}

	e["REDASH_PROD_DATA_SOURCES"] = "3,-1"
	if _, err := Load(envMap(e)); err == nil {
		t.Fatal("a negative data source id should be refused")
	}
}

func TestBadTierFailsClosed(t *testing.T) {
	e := baseEnv(t)
	e["REDASH_TIER"] = "admin"
	if _, err := Load(envMap(e)); err == nil {
		t.Fatal("an unknown tier must be an error, not a silent fallback to read")
	}
}

func TestNumericBoundsAreEnforced(t *testing.T) {
	for key, bad := range map[string]string{
		"REDASH_MAX_ROWS":       "0",
		"REDASH_MAX_BYTES":      "10",
		"REDASH_MAX_CELL_CHARS": "1",
		"REDASH_RATE_LIMIT":     "-5",
		"REDASH_TIMEOUT":        "10m",
	} {
		e := baseEnv(t)
		e[key] = bad
		if _, err := Load(envMap(e)); err == nil {
			t.Errorf("%s=%s should be refused", key, bad)
		}
	}
}

// TestDefaultRedactDoesNotEatOrdinaryColumns is the regression test for the
// obvious way to get this wrong: "pan" is a substring of "company", and a
// naive pattern would redact half a lending schema.
func TestDefaultRedactDoesNotEatOrdinaryColumns(t *testing.T) {
	c, err := Load(envMap(baseEnv(t)))
	if err != nil {
		t.Fatal(err)
	}

	shouldRedact := []string{
		"pan", "PAN", "pan_number", "customer_pan", "aadhaar",
		"email", "primary_email", "phone", "mobile", "card_no",
		"account_no", "dob", "ifsc", "upi", "cvv",
	}
	for _, col := range shouldRedact {
		if !c.Redact.MatchString(col) {
			t.Errorf("column %q should be redacted by default", col)
		}
	}

	shouldNotRedact := []string{
		"company", "company_name", "panel_id", "expanded", "loan_id",
		"disbursal_amount", "status", "created_at", "emailer_template",
		"cardinality", "microphone_test",
	}
	for _, col := range shouldNotRedact {
		if c.Redact.MatchString(col) {
			t.Errorf("column %q must NOT be redacted by the default pattern", col)
		}
	}
}

func TestTargetsCarryEnforcementFlag(t *testing.T) {
	c, err := Load(envMap(baseEnv(t)))
	if err != nil {
		t.Fatal(err)
	}
	tgt, ok := c.Targets()["prod"]
	if !ok {
		t.Fatal("prod target missing")
	}
	if !tgt.EnforcedReadOnly {
		t.Error("the enforcement flag should reach the target")
	}
	if tgt.BaseURL() != "https://redash.invalid" {
		t.Errorf("BaseURL = %q", tgt.BaseURL())
	}
}
