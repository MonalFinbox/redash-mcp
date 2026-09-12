package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOneJSONLinePerRecord(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, func() time.Time { return time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC) })

	l.Log(Record{Tool: "redash_get_query", Instance: "uat", Args: map[string]any{"query_id": 7}, Outcome: "ok", Rows: 3})
	l.Log(Record{Tool: "redash_get_schema", Instance: "prod", Outcome: "error", Error: "withheld"})

	sc := bufio.NewScanner(&buf)
	var got []Record
	for sc.Scan() {
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("line is not JSON: %q", sc.Text())
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 lines, got %d", len(got))
	}
	if got[0].Time != "2026-09-12T12:00:00Z" || got[0].Tool != "redash_get_query" || got[1].Outcome != "error" {
		t.Errorf("records did not round trip: %+v", got)
	}
}

func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Log(Record{Tool: "redash_list_queries", Args: map[string]any{"search": strings.Repeat("x", 500)}, Outcome: "ok"})
		}()
	}
	wg.Wait()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("an interleaved line is not valid JSON: %.80q", line)
		}
	}
}

func TestFileDestinationIsOwnerOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	l.Log(Record{Tool: "redash_list_data_sources", Outcome: "ok"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("audit file mode = %04o, want 0600", fi.Mode().Perm())
	}
}

func TestLooseExistingFileAndStdoutAreRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("a group-readable audit file must be refused, got %v", err)
	}
	if _, err := Open("stdout"); err == nil {
		t.Error("stdout must be refused because the stdio transport owns it")
	}
	for _, d := range []string{"", "stderr", "off"} {
		if _, err := Open(d); err != nil {
			t.Errorf("Open(%q): %v", d, err)
		}
	}
}
