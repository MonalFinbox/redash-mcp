package policy

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// netPackages are the ways a Go program reaches the network. Only
// internal/policy may name them, so that the set of destinations this
// binary can talk to stays reviewable in one directory.
var netPackages = map[string]bool{
	"net/http": true,
	"net":      true,
	"net/rpc":  true,
	"os/exec":  true, // shelling out is another way to make a request
}

// policyDir is the one package exempt from the rule.
var policyDir = filepath.Join("internal", "policy")

func TestOnlyPolicyPackageReachesTheNetwork(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if filepath.Dir(rel) == policyDir {
			return nil
		}

		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if netPackages[p] {
				t.Errorf("%s imports %q, but only %s may reach the network; "+
					"route this through policy.Guard instead", rel, p, policyDir)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEveryEndpointIsRegistered guards against declaring an Endpoint var and
// forgetting to add it to all, which would make it silently unreachable,
// or worse, reachable but invisible to the audit tooling.
func TestEveryEndpointIsRegistered(t *testing.T) {
	declared := []Endpoint{
		ListQueries, GetQuery, GetQueryResults, GetResultByID,
		ListDashboards, GetDashboard, ListDataSources, GetSchema,
		RunSavedQuery,
	}
	if len(All()) != len(declared) {
		t.Fatalf("All() has %d endpoints, %d are declared in this test; "+
			"a new endpoint was added without registering it", len(All()), len(declared))
	}
	for _, ep := range declared {
		if !isRegistered(ep) {
			t.Errorf("endpoint %q is declared but missing from all", ep.Name())
		}
	}
}

// TestNoMutatingEndpointIsReadTier stops the most damaging possible typo:
// a write verb accidentally marked TierRead, which would make it reachable
// in the default configuration.
func TestNoMutatingEndpointIsReadTier(t *testing.T) {
	for _, ep := range All() {
		if ep.Method() != "GET" && ep.Tier() == TierRead {
			t.Errorf("endpoint %q is %s but sits at TierRead; "+
				"non-GET endpoints must never be reachable by default",
				ep.Name(), ep.Method())
		}
	}
}
