// Command redash-mcp is a read-only Model Context Protocol server for Redash.
//
// At this milestone it validates configuration and reports exactly what the
// process would be permitted to do. The MCP server itself arrives in M2.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/MonalFinbox/redash-mcp/internal/config"
	"github.com/MonalFinbox/redash-mcp/internal/policy"
)

// Version is stamped at build time with -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	check := flag.Bool("check", false, "validate the configuration, print what this server could do, and exit")
	envFile := flag.String("env-file", "", "read REDASH_ settings from this chmod 600 file; the process environment still wins")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *version {
		fmt.Println("redash-mcp", Version)
		return
	}

	env := config.Env(os.Getenv)
	if *envFile != "" {
		fileEnv, err := config.LoadEnvFile(*envFile, env)
		if err != nil {
			fmt.Fprintf(os.Stderr, "redash-mcp: configuration error\n\n%v\n", err)
			os.Exit(1)
		}
		env = fileEnv
	}

	cfg, err := config.Load(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "redash-mcp: configuration error\n\n%v\n", err)
		os.Exit(1)
	}

	writeBanner(os.Stderr, cfg)

	if *check {
		fmt.Fprintln(os.Stderr, "Configuration is valid.")
		return
	}

	fmt.Fprintln(os.Stderr, "The MCP server is not wired up yet (arrives in M2).")
	fmt.Fprintln(os.Stderr, "Run with --check to validate your setup in the meantime.")
	os.Exit(1)
}

func writeBanner(w io.Writer, cfg *config.Config) {
	fmt.Fprintf(w, "redash-mcp %s\n", Version)
	fmt.Fprintf(w, "tier          %s\n", cfg.Tier)
	fmt.Fprintf(w, "limits        %d rows · %s · %d chars per cell · %d req/min · %s timeout\n",
		cfg.MaxRows, humanBytes(cfg.MaxBytes), cfg.MaxCellChars, cfg.RateLimit, cfg.Timeout)
	fmt.Fprintf(w, "redaction     %s\n", cfg.Redact.String())
	fmt.Fprintf(w, "audit log     %s\n", cfg.AuditLog)
	if cfg.DefaultInstance != "" {
		fmt.Fprintf(w, "default       %s (used when a tool call names no instance)\n", cfg.DefaultInstance)
	} else {
		fmt.Fprintln(w, "default       none (every tool call must name an instance)")
	}

	fmt.Fprintln(w, "\ninstances")
	for _, name := range cfg.Order {
		inst := cfg.Instances[name]
		layers := "1 layer  (this server's policy guard only)"
		if inst.EnforcedReadOnly {
			layers = "2 layers (View Only Redash account + policy guard)"
		}
		fmt.Fprintf(w, "  %-8s %s\n", name, inst.URL)
		fmt.Fprintf(w, "  %-8s read-only enforced by %s\n", "", layers)
		if len(inst.DataSources) > 0 {
			fmt.Fprintf(w, "  %-8s data sources limited to %s\n", "", joinInts(inst.DataSources))
		}
	}

	fmt.Fprintln(w, "\nreachable endpoints")
	eps := policy.All()
	sort.Slice(eps, func(i, j int) bool { return eps[i].Name() < eps[j].Name() })
	for _, ep := range eps {
		state := "reachable"
		switch {
		case ep.Tier() > cfg.Tier:
			state = "blocked, needs tier " + ep.Tier().String()
		case ep.Method() != "GET":
			state = "blocked, no non-GET code path exists"
		}
		fmt.Fprintf(w, "  %-4s %-32s %s\n", ep.Method(), ep.Path(), state)
	}

	if len(cfg.Warnings) > 0 {
		fmt.Fprintln(w, "\nwarnings")
		for _, warn := range cfg.Warnings {
			fmt.Fprintf(w, "  · %s\n", strings.ReplaceAll(warn, "\n", "\n    "))
		}
	}
	fmt.Fprintln(w)
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.Itoa(n/(1<<20)) + " MiB"
	case n >= 1<<10:
		return strconv.Itoa(n/(1<<10)) + " KiB"
	}
	return strconv.Itoa(n) + " B"
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ", ")
}
