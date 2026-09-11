package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnvFile reads REDASH_ settings from a file and returns an Env that
// consults the process environment first and the file second.
//
// It exists so an MCP client's configuration can name one file instead of
// repeating every setting inline. The file is held to the same rule as a key
// file, owner-only, since it may carry an inline key.
//
// The format is deliberately narrow: KEY=VALUE lines, an optional leading
// "export ", "#" comment lines, and optional matching quotes around a value.
// There are no inline comments, because a redaction regex can contain "#".
// Error messages name the line and the key, never the value.
func LoadEnvFile(path string, base Env) (Env, error) {
	if base == nil {
		base = os.Getenv
	}
	p, err := expandHome(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("env file: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf(
			"env file %s is readable by group or others (mode %04o). Fix it with:\n    chmod 600 %s",
			p, fi.Mode().Perm(), p)
	}

	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("env file: %w", err)
	}
	defer f.Close()

	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("env file %s line %d: expected KEY=VALUE", p, n)
		}
		if !strings.HasPrefix(k, "REDASH_") {
			return nil, fmt.Errorf("env file %s line %d: %q is not a REDASH_ setting", p, n, k)
		}
		if _, dup := vals[k]; dup {
			return nil, fmt.Errorf("env file %s line %d: %s is set twice", p, n, k)
		}
		vals[k] = unquote(strings.TrimSpace(v))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("env file %s: %w", p, err)
	}

	return func(k string) string {
		if v := base(k); v != "" {
			return v
		}
		return vals[k]
	}, nil
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}
