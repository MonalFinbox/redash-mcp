package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvFileParsesSettings(t *testing.T) {
	p := writeEnvFile(t, strings.Join([]string{
		"# instances",
		"",
		"REDASH_INSTANCES=prod",
		`export REDASH_PROD_URL="https://redash.invalid"`,
		"REDASH_TIER='read'",
		"REDASH_REDACT_COLUMNS=(?i)^(pan|x#y)$",
	}, "\n"), 0o600)

	env, err := LoadEnvFile(p, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"REDASH_INSTANCES":      "prod",
		"REDASH_PROD_URL":       "https://redash.invalid",
		"REDASH_TIER":           "read",
		"REDASH_REDACT_COLUMNS": "(?i)^(pan|x#y)$",
		"REDASH_UNSET":          "",
	} {
		if got := env(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestEnvFileProcessEnvironmentWins(t *testing.T) {
	p := writeEnvFile(t, "REDASH_TIER=read\nREDASH_MAX_ROWS=50\n", 0o600)
	env, err := LoadEnvFile(p, envMap(map[string]string{"REDASH_MAX_ROWS": "10"}))
	if err != nil {
		t.Fatal(err)
	}
	if env("REDASH_MAX_ROWS") != "10" {
		t.Errorf("the process environment should override the file, got %q", env("REDASH_MAX_ROWS"))
	}
	if env("REDASH_TIER") != "read" {
		t.Errorf("unset process values should fall through to the file, got %q", env("REDASH_TIER"))
	}
}

func TestEnvFileMustBePrivate(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		p := writeEnvFile(t, "REDASH_INSTANCES=prod\n", mode)
		_, err := LoadEnvFile(p, envMap(nil))
		if err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %04o: want an error naming chmod 600, got %v", mode, err)
		}
	}
}

func TestEnvFileRejectsBadLinesWithoutEchoingValues(t *testing.T) {
	cases := map[string]string{
		"foreign key":  "AWS_SECRET_ACCESS_KEY=hunter2-value\n",
		"no equals":    "REDASH_PROD_API_KEY\n",
		"empty key":    "=hunter2-value\n",
		"set twice":    "REDASH_PROD_API_KEY=hunter2-value\nREDASH_PROD_API_KEY=hunter2-value\n",
		"foreign late": "REDASH_INSTANCES=prod\nGITHUB_TOKEN=hunter2-value\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadEnvFile(writeEnvFile(t, body, 0o600), envMap(nil))
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), "hunter2-value") {
				t.Fatalf("error echoed a value: %v", err)
			}
		})
	}
}

func TestEnvFileFeedsLoad(t *testing.T) {
	p := writeEnvFile(t, strings.Join([]string{
		"REDASH_INSTANCES=prod,uat",
		"REDASH_DEFAULT_INSTANCE=uat",
		"REDASH_PROD_URL=https://redash.invalid",
		"REDASH_PROD_API_KEY_FILE=" + keyFile(t, 0o600),
		"REDASH_PROD_ENFORCED_READONLY=true",
		"REDASH_PROD_DATA_SOURCES=3,7",
		"REDASH_UAT_URL=https://redash-uat.invalid",
		"REDASH_UAT_API_KEY_FILE=" + keyFile(t, 0o600),
		"REDASH_UAT_DATA_SOURCES=11,12,13",
	}, "\n"), 0o600)

	env, err := LoadEnvFile(p, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultInstance != "uat" {
		t.Errorf("DefaultInstance = %q", c.DefaultInstance)
	}
	if got := c.Instances["uat"].DataSources; len(got) != 3 || got[2] != 13 {
		t.Errorf("uat data sources = %v", got)
	}
}
