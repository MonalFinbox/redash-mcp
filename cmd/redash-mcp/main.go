// Command redash-mcp is a read-only Model Context Protocol server for Redash.
//
// With no flags it speaks MCP over stdio: stdout carries the protocol and
// everything meant for a person goes to stderr. --check validates the setup
// and prints what the server would be permitted to do, without contacting
// Redash.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MonalFinbox/redash-mcp/internal/audit"
	"github.com/MonalFinbox/redash-mcp/internal/config"
	"github.com/MonalFinbox/redash-mcp/internal/policy"
	"github.com/MonalFinbox/redash-mcp/internal/redash"
	"github.com/MonalFinbox/redash-mcp/internal/server"
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

	if *check {
		writeBanner(os.Stderr, cfg)
		fmt.Fprintln(os.Stderr, "Configuration is valid.")
		return
	}

	if err := serve(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "redash-mcp: %v\n", err)
		os.Exit(1)
	}
}

// serve runs the MCP server on stdio until the client disconnects or the
// process is signalled.
func serve(cfg *config.Config) error {
	log, err := audit.Open(cfg.AuditLog)
	if err != nil {
		return err
	}
	defer log.Close()

	guard := policy.NewGuard(policy.Options{Tier: cfg.Tier, Timeout: cfg.Timeout})
	instances := make(map[string]redash.Instance, len(cfg.Instances))
	for name, target := range cfg.Targets() {
		instances[name] = redash.Instance{Target: target, DataSources: cfg.Instances[name].DataSources}
	}
	client := redash.New(redash.Options{
		Guard:           guard,
		Instances:       instances,
		DefaultInstance: cfg.DefaultInstance,
		RateLimit:       cfg.RateLimit,
	})

	srv := server.New(server.Options{Config: cfg, Client: client, Audit: log, Version: Version})

	fmt.Fprintf(os.Stderr, "redash-mcp %s: serving %s over stdio at tier %s\n",
		Version, strings.Join(cfg.Order, ", "), cfg.Tier)
	for _, w := range cfg.Warnings {
		fmt.Fprintf(os.Stderr, "redash-mcp: warning: %s\n", strings.ReplaceAll(w, "\n", " "))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
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
		} else {
			fmt.Fprintf(w, "  %-8s data sources: every one this key can see (no allowlist)\n", "")
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

	fmt.Fprintln(w, "\ntools")
	for _, name := range server.ToolNames() {
		fmt.Fprintf(w, "  %s\n", name)
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
