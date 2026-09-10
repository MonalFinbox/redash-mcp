// Package config loads and validates the server's settings.
//
// Everything here fails closed: a setting that cannot be understood is an
// error rather than a fallback, because a silently-ignored cap or redaction
// rule is worse than a server that refuses to start.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MonalFinbox/redash-mcp/internal/policy"
)

const (
	DefaultMaxRows      = 200
	DefaultMaxBytes     = 256 << 10
	DefaultMaxCellChars = 512
	DefaultRateLimit    = 30
	DefaultTimeout      = 20 * time.Second

	// DefaultRedact anchors each term on a word boundary of its own. A bare
	// substring match would be worse than useless here: "pan" is a
	// substring of "company", so it would redact half a lending schema.
	DefaultRedact = `(?i)(^|_)(pan|aadhaar|aadhar|email|phone|mobile|card_no|card_number|` +
		`account_no|acct_no|dob|ifsc|upi|ssn|passport|cvv)($|_)`
)

// Instance is one configured Redash deployment.
type Instance struct {
	Name   string
	URL    *url.URL
	APIKey string

	// EnforcedReadOnly declares that the Redash account behind APIKey sits
	// in a View Only group and so cannot write, whatever this program does.
	// Instances without it are protected by one layer instead of two, and
	// the startup banner says so.
	EnforcedReadOnly bool

	// DataSources, when non-empty, is the set of Redash data source ids the
	// tools may touch. Useful when one Redash mixes prod, UAT and shared
	// databases under near-identical names.
	DataSources []int
}

type Config struct {
	Order        []string
	Instances    map[string]*Instance
	Tier         policy.Tier
	MaxRows      int
	MaxBytes     int
	MaxCellChars int
	Redact       *regexp.Regexp
	Timeout      time.Duration
	RateLimit    int
	AllowPrivate bool
	AuditLog     string

	// Warnings are conditions worth telling the user about that are not
	// severe enough to refuse to start.
	Warnings []string
}

// Env is the environment lookup, injectable so tests never touch os.Environ.
type Env func(string) string

var instanceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// Load reads configuration from the environment.
func Load(env Env) (*Config, error) {
	if env == nil {
		env = os.Getenv
	}

	c := &Config{
		Instances:    map[string]*Instance{},
		MaxRows:      DefaultMaxRows,
		MaxBytes:     DefaultMaxBytes,
		MaxCellChars: DefaultMaxCellChars,
		RateLimit:    DefaultRateLimit,
		Timeout:      DefaultTimeout,
	}

	var err error
	if c.Tier, err = policy.ParseTier(strings.TrimSpace(env("REDASH_TIER"))); err != nil {
		return nil, err
	}
	if c.AllowPrivate, err = envBool(env, "REDASH_ALLOW_PRIVATE_ADDRS", false); err != nil {
		return nil, err
	}
	if c.MaxRows, err = envInt(env, "REDASH_MAX_ROWS", DefaultMaxRows, 1, 100_000); err != nil {
		return nil, err
	}
	if c.MaxBytes, err = envInt(env, "REDASH_MAX_BYTES", DefaultMaxBytes, 1024, 16<<20); err != nil {
		return nil, err
	}
	if c.MaxCellChars, err = envInt(env, "REDASH_MAX_CELL_CHARS", DefaultMaxCellChars, 16, 1<<20); err != nil {
		return nil, err
	}
	if c.RateLimit, err = envInt(env, "REDASH_RATE_LIMIT", DefaultRateLimit, 1, 10_000); err != nil {
		return nil, err
	}
	if c.Timeout, err = envDuration(env, "REDASH_TIMEOUT", DefaultTimeout); err != nil {
		return nil, err
	}

	redactSrc := strings.TrimSpace(env("REDASH_REDACT_COLUMNS"))
	if redactSrc == "" {
		redactSrc = DefaultRedact
	}
	if c.Redact, err = regexp.Compile(redactSrc); err != nil {
		return nil, fmt.Errorf("REDASH_REDACT_COLUMNS is not a valid regular expression: %w", err)
	}

	c.AuditLog = strings.TrimSpace(env("REDASH_AUDIT_LOG"))
	if c.AuditLog == "" {
		c.AuditLog = "stderr"
	}

	names := splitList(env("REDASH_INSTANCES"))
	if len(names) == 0 {
		return nil, fmt.Errorf(
			"REDASH_INSTANCES is empty.\nSet it to a comma-separated list of short names, e.g. REDASH_INSTANCES=prod,uat, then define REDASH_PROD_URL and REDASH_PROD_API_KEY_FILE for each")
	}

	seen := map[string]bool{}
	for _, name := range names {
		if !instanceNamePattern.MatchString(name) {
			return nil, fmt.Errorf("instance name %q is invalid: use lowercase letters, digits and underscores, starting with a letter", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("instance %q is listed twice in REDASH_INSTANCES", name)
		}
		seen[name] = true

		inst, warns, err := loadInstance(env, name, c.AllowPrivate)
		if err != nil {
			return nil, fmt.Errorf("instance %q: %w", name, err)
		}
		c.Warnings = append(c.Warnings, warns...)
		c.Instances[name] = inst
		c.Order = append(c.Order, name)
	}

	for _, inst := range c.Instances {
		if !inst.EnforcedReadOnly {
			c.Warnings = append(c.Warnings, fmt.Sprintf(
				"instance %q is not marked as an enforced read-only account, so this server's policy guard is the only thing preventing a write. "+
					"Point it at a Redash user in a View Only group and set REDASH_%s_ENFORCED_READONLY=true",
				inst.Name, envKey(inst.Name)))
		}
	}
	return c, nil
}

func loadInstance(env Env, name string, allowPrivate bool) (*Instance, []string, error) {
	k := envKey(name)
	inst := &Instance{Name: name}

	u, warns, err := policy.ValidateBaseURL(env("REDASH_"+k+"_URL"), allowPrivate)
	if err != nil {
		return nil, nil, fmt.Errorf("REDASH_%s_URL: %w", k, err)
	}
	inst.URL = u

	inst.APIKey, err = loadKey(env, k)
	if err != nil {
		return nil, nil, err
	}

	if inst.EnforcedReadOnly, err = envBool(env, "REDASH_"+k+"_ENFORCED_READONLY", false); err != nil {
		return nil, nil, err
	}

	for _, raw := range splitList(env("REDASH_" + k + "_DATA_SOURCES")) {
		id, convErr := strconv.Atoi(raw)
		if convErr != nil || id <= 0 {
			return nil, nil, fmt.Errorf("REDASH_%s_DATA_SOURCES: %q is not a positive data source id", k, raw)
		}
		inst.DataSources = append(inst.DataSources, id)
	}
	return inst, warns, nil
}

// loadKey prefers a key file over an inline env var, and refuses a key file
// that anyone but the owner can read.
func loadKey(env Env, k string) (string, error) {
	if path := strings.TrimSpace(env("REDASH_" + k + "_API_KEY_FILE")); path != "" {
		p, err := expandHome(path)
		if err != nil {
			return "", err
		}
		fi, err := os.Stat(p)
		if err != nil {
			return "", fmt.Errorf("REDASH_%s_API_KEY_FILE: %w", k, err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf(
				"key file %s is readable by group or others (mode %04o). Fix it with:\n    chmod 600 %s",
				p, fi.Mode().Perm(), p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("REDASH_%s_API_KEY_FILE: %w", k, err)
		}
		key := strings.TrimSpace(string(b))
		if key == "" {
			return "", fmt.Errorf("key file %s is empty", p)
		}
		return key, nil
	}

	if key := strings.TrimSpace(env("REDASH_" + k + "_API_KEY")); key != "" {
		return key, nil
	}

	return "", fmt.Errorf(
		"no API key: set REDASH_%s_API_KEY_FILE to a chmod 600 file holding the key (preferred), or REDASH_%s_API_KEY", k, k)
}

// Targets converts the configuration into the form the guard accepts.
func (c *Config) Targets() map[string]policy.Target {
	out := make(map[string]policy.Target, len(c.Instances))
	for name, inst := range c.Instances {
		out[name] = policy.NewTarget(name, inst.URL, inst.APIKey, inst.EnforcedReadOnly)
	}
	return out
}

// envKey turns an instance name into its environment variable infix.
func envKey(name string) string { return strings.ToUpper(name) }

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envBool(env Env, key string, def bool) (bool, error) {
	raw := strings.TrimSpace(env(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not a boolean (use true or false)", key, raw)
	}
	return v, nil
}

func envInt(env Env, key string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(env(key))
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", key, raw)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("%s: %d is outside the permitted range %d..%d", key, v, min, max)
	}
	return v, nil
}

func envDuration(env Env, key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(env(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration (try 20s)", key, raw)
	}
	if d <= 0 || d > 5*time.Minute {
		return 0, fmt.Errorf("%s: %s is outside the permitted range 1s..5m", key, d)
	}
	return d, nil
}

func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	return p, nil
}
